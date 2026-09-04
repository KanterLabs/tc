#!/usr/bin/env python3
"""Unit tests for the Helm helper."""

from __future__ import annotations

import argparse
import contextlib
import io
import json
import os
import tempfile
import unittest
import uuid
from email.message import Message
from pathlib import Path
from unittest import mock

import helm as helper
import helm_session as session


class ConfigTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls._config_root = tempfile.TemporaryDirectory(prefix="helm-config-tests-")

    @classmethod
    def tearDownClass(cls) -> None:
        cls._config_root.cleanup()

    def setUp(self) -> None:
        self._config_defaults = [
            mock.patch.object(helper, "DEFAULT_CONFIG", Path(self._config_root.name) / "helm.json"),
            mock.patch.object(helper, "LEGACY_CONFIG", Path(self._config_root.name) / "tc-roadmap.json"),
        ]
        for patcher in self._config_defaults:
            patcher.start()

    def tearDown(self) -> None:
        for patcher in self._config_defaults:
            patcher.stop()

    def test_loads_mode_0600_config_without_exposing_values(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / "credentials.json"
            path.write_text(
                json.dumps({"base_url": "https://tc.example", "token": "private-token"}),
                encoding="utf-8",
            )
            path.chmod(0o600)
            with mock.patch.dict(os.environ, {"HELM_CONFIG": str(path)}, clear=True):
                config = helper.load_config()
            self.assertEqual(config.base_url, "https://tc.example")
            self.assertEqual(config.token, "private-token")

    def test_reads_legacy_credential_file_as_a_read_only_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / "credentials.json"
            path.write_text(json.dumps({"base_url": "https://legacy.example", "token": "legacy-token"}), encoding="utf-8")
            path.chmod(0o600)
            with mock.patch.dict(os.environ, {"TC_ROADMAP_CONFIG": str(path)}, clear=True):
                config = helper.load_config()
            self.assertEqual(config.base_url, "https://legacy.example")
            self.assertEqual(config.token, "legacy-token")

    def test_conflicting_credential_file_aliases_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            canonical = Path(raw) / "helm.json"
            legacy = Path(raw) / "roadmap.json"
            canonical.write_text("{}", encoding="utf-8")
            legacy.write_text("{}", encoding="utf-8")
            canonical.chmod(0o600)
            legacy.chmod(0o600)
            with mock.patch.dict(
                os.environ,
                {"HELM_CONFIG": str(canonical), "TC_ROADMAP_CONFIG": str(legacy)},
                clear=True,
            ):
                with self.assertRaisesRegex(helper.HelmError, "conflicting"):
                    helper.load_config()

    def test_rejects_broad_credential_permissions(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / "credentials.json"
            path.write_text('{"token":"private-token"}', encoding="utf-8")
            path.chmod(0o644)
            with mock.patch.dict(os.environ, {"HELM_CONFIG": str(path)}, clear=True):
                with self.assertRaisesRegex(helper.HelmError, "permissions are too broad"):
                    helper.load_config()

    def test_rejects_remote_plain_http(self) -> None:
        with mock.patch.dict(
            os.environ,
            {"HELM_URL": "http://tc.example", "HELM_TOKEN": "private-token"},
            clear=True,
        ):
            with self.assertRaisesRegex(helper.HelmError, "must use HTTPS"):
                helper.load_config()

    def test_legacy_environment_aliases_are_supported(self) -> None:
        with mock.patch.dict(
            os.environ,
            {"TC_ROADMAP_URL": "https://legacy.example", "TC_ROADMAP_TOKEN": "legacy-token"},
            clear=True,
        ):
            config = helper.load_config()
        self.assertEqual(config.base_url, "https://legacy.example")
        self.assertEqual(config.token, "legacy-token")

        with mock.patch.dict(
            os.environ,
            {"TC_ROADMAP_URL": "https://legacy.example", "ROADMAP_TOKEN": "legacy-token"},
            clear=True,
        ):
            config = helper.load_config()
        self.assertEqual(config.base_url, "https://legacy.example")
        self.assertEqual(config.token, "legacy-token")

    def test_canonical_environment_wins_only_when_aliases_agree(self) -> None:
        with mock.patch.dict(
            os.environ,
            {
                "HELM_URL": "https://helm.example",
                "TC_ROADMAP_URL": "https://helm.example/",
                "HELM_TOKEN": "same-token",
                "TC_ROADMAP_TOKEN": "same-token",
                "ROADMAP_TOKEN": "same-token",
            },
            clear=True,
        ):
            config = helper.load_config()
        self.assertEqual(config.base_url, "https://helm.example")
        self.assertEqual(config.token, "same-token")

    def test_conflicting_canonical_and_legacy_environment_fails_closed(self) -> None:
        with mock.patch.dict(
            os.environ,
            {"HELM_URL": "https://helm.example", "TC_ROADMAP_URL": "https://legacy.example", "HELM_TOKEN": "same-token"},
            clear=True,
        ):
            with self.assertRaisesRegex(helper.HelmError, "conflicting"):
                helper.load_config()
        with mock.patch.dict(
            os.environ,
            {"HELM_URL": "https://helm.example", "HELM_TOKEN": "helm-token", "ROADMAP_TOKEN": "legacy-token"},
            clear=True,
        ):
            with self.assertRaisesRegex(helper.HelmError, "conflicting"):
                helper.load_config()

    def test_cloudflare_aliases_follow_the_same_precedence_rules(self) -> None:
        with mock.patch.dict(
            os.environ,
            {
                "HELM_URL": "https://helm.example",
                "HELM_TOKEN": "same-token",
                "HELM_CF_ACCESS_CLIENT_ID": "client-id",
                "TC_CF_ACCESS_CLIENT_ID": "client-id",
                "HELM_CF_ACCESS_CLIENT_SECRET": "client-secret",
                "TC_CF_ACCESS_CLIENT_SECRET": "client-secret",
            },
            clear=True,
        ):
            config = helper.load_config()
        self.assertEqual(config.cf_access_client_id, "client-id")
        self.assertEqual(config.cf_access_client_secret, "client-secret")


class CommandTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls._state_root = tempfile.TemporaryDirectory(prefix="helm-tests-")

    @classmethod
    def tearDownClass(cls) -> None:
        cls._state_root.cleanup()

    def setUp(self) -> None:
        self._state_env = mock.patch.dict(
            os.environ,
            {
                "CODEX_HOME": self._state_root.name,
                "HELM_STATE_DIR": str(Path(self._state_root.name) / "state"),
                "TC_ROADMAP_STATE_DIR": str(Path(self._state_root.name) / "legacy-state"),
            },
            clear=False,
        )
        self._state_env.start()

    def tearDown(self) -> None:
        self._state_env.stop()

    def test_idempotency_is_stable_and_action_specific(self) -> None:
        first = helper._idempotency("run-1", "progress", "validated")
        self.assertEqual(first, helper._idempotency("run-1", "progress", "validated"))
        self.assertNotEqual(first, helper._idempotency("run-1", "complete", "validated"))
        self.assertNotIn("validated", first)

    def test_structured_progress_message_is_human_readable(self) -> None:
        args = mock.Mock(
            message="API is complete",
            state="verifying",
            phase="Backend",
            completed=2,
            total=3,
            next_step="Run the browser suite",
        )
        self.assertEqual(
            helper._progress_message(args),
            "Agent update · Verifying · Backend · 2/3\n\nAPI is complete\n\nNext: Run the browser suite",
        )

    def test_plain_progress_message_remains_compatible(self) -> None:
        args = mock.Mock(message="Validated", state=None, phase=None, completed=None, total=None, next_step=None)
        self.assertEqual(helper._progress_message(args), "Validated")

    def test_progress_rejects_partial_or_invalid_counts(self) -> None:
        partial = mock.Mock(message="Working", state=None, phase=None, completed=1, total=None, next_step=None)
        with self.assertRaisesRegex(helper.HelmError, "provided together"):
            helper._progress_message(partial)
        invalid = mock.Mock(message="Working", state=None, phase=None, completed=4, total=3, next_step=None)
        with self.assertRaisesRegex(helper.HelmError, "0 <= completed"):
            helper._progress_message(invalid)

    def test_structured_progress_posts_payload_with_version_and_canonical_idempotency(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []
        current = {
            "id": "task-1",
            "key": "TC-1",
            "version": 7,
            "claimed_by": "agent-1",
        }
        updated = {**current, "version": 8}

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method == "GET":
                    return current, {}
                return updated, {}

        args = argparse.Namespace(
            task="TC-1",
            message="API is complete",
            state=None,
            phase="Backend",
            completed=1,
            total=2,
            next_step="Run the browser suite",
            checkpoint_refs=["tests/unit", "tests/browser"],
            operation_id="run-1",
        )
        result = helper.cmd_progress(StubClient(), args)  # type: ignore[arg-type]

        expected = {
            "operation_id": "run-1",
            "state": "working",
            "phase": "Backend",
            "summary": "API is complete",
            "next_action": "Run the browser suite",
            "checkpoint_completed": 1,
            "checkpoint_total": 2,
            "checkpoint_refs": ["tests/unit", "tests/browser"],
        }
        self.assertEqual(result, {"task": updated, "operation_id": "run-1"})
        self.assertEqual(calls[0][0:2], ("GET", "/tasks/TC-1"))
        self.assertEqual(calls[1][0:2], ("POST", "/tasks/task-1/progress"))
        self.assertEqual(calls[1][2]["body"], expected)
        self.assertEqual(calls[1][2]["if_match"], 7)
        self.assertEqual(
            calls[1][2]["idempotency_key"],
            helper._mutation_idempotency("run-1", "POST", "/tasks/task-1/progress", expected),
        )

    def test_plain_progress_uses_legacy_comments_endpoint(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method == "GET":
                    return {"id": "task-1", "key": "TC-1", "version": 2}, {}
                return {"id": "comment-1", "body": "Validated"}, {}

        args = argparse.Namespace(
            task="TC-1",
            message="  Validated  ",
            state=None,
            phase=None,
            completed=None,
            total=None,
            next_step=None,
            checkpoint_refs=[],
            operation_id="run-2",
        )
        result = helper.cmd_progress(StubClient(), args)  # type: ignore[arg-type]

        self.assertEqual(result["task"], "TC-1")
        self.assertEqual(calls[1][0:2], ("POST", "/tasks/task-1/comments"))
        self.assertEqual(calls[1][2]["body"], {"body": "Validated"})
        self.assertNotIn("if_match", calls[1][2])
        self.assertNotIn("/progress", calls[1][1])

    def test_structured_progress_requires_a_claim(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                return {"id": "task-1", "key": "TC-1", "version": 2}, {}

        args = argparse.Namespace(
            task="TC-1",
            message="Working",
            state="working",
            phase=None,
            completed=None,
            total=None,
            next_step=None,
            checkpoint_refs=[],
            operation_id="run-3",
        )
        with self.assertRaisesRegex(helper.HelmError, "actively claimed"):
            helper.cmd_progress(StubClient(), args)  # type: ignore[arg-type]
        self.assertEqual(len(calls), 1)

    def test_structured_progress_validates_counts_and_normalizes_refs(self) -> None:
        args = argparse.Namespace(
            operation_id="run-4",
            message="Working",
            state="working",
            phase=None,
            completed=3,
            total=2,
            next_step=None,
            checkpoint_refs=["  tests/unit  "],
        )
        with self.assertRaisesRegex(helper.HelmError, "0 <= completed"):
            helper._structured_progress_payload(args)

        args.completed, args.total = 1, 1
        self.assertEqual(helper._structured_progress_payload(args)["checkpoint_refs"], ["tests/unit"])

    def test_progress_parser_accepts_repeatable_checkpoint_refs(self) -> None:
        args = helper.build_parser().parse_args(
            [
                "progress",
                "--task",
                "TC-1",
                "--message",
                "Working",
                "--checkpoint-ref",
                "tests/unit",
                "--checkpoint-ref",
                "tests/browser",
                "--operation-id",
                "run-5",
            ]
        )
        self.assertEqual(args.checkpoint_refs, ["tests/unit", "tests/browser"])

    def test_backlog_creates_unclaimed_task_in_backlog_column(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if path == "/projects?limit=200":
                    return {"data": [{"id": "project-1", "key": "TC"}]}, {}
                if path.endswith("/columns?limit=200"):
                    return {"data": [{"id": "column-1", "semantic_state": "backlog"}]}, {}
                return {"id": "task-1", "key": "TC-2", "version": 1}, {}

        args = mock.Mock(
            project="TC",
            title="Show agent pulse",
            goal="Explain current work",
            step=["Show the current phase"],
            priority="high",
            operation_id="plan-1",
        )
        result = helper.cmd_backlog(StubClient(), args)  # type: ignore[arg-type]

        self.assertEqual(result["task"]["key"], "TC-2")
        self.assertEqual(calls[-1][0], "POST")
        self.assertEqual(calls[-1][2]["body"]["column_id"], "column-1")
        self.assertIn("Acceptance criteria\n- [ ] Show the current phase", calls[-1][2]["body"]["description"])
        # The mutation namespace remains stable across the Helm rename so a
        # retried operation cannot create a duplicate Helm action.
        self.assertTrue(str(calls[-1][2]["idempotency_key"]).startswith("helm-"))
        self.assertFalse(any(path.endswith("/claim") for _, path, _ in calls))

    def test_backlog_requires_a_backlog_column(self) -> None:
        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                if path == "/projects?limit=200":
                    return {"data": [{"id": "project-1", "key": "TC"}]}, {}
                return {"data": [{"id": "column-1", "semantic_state": "active"}]}, {}

        args = mock.Mock(project="TC", operation_id="plan-2")
        with self.assertRaisesRegex(helper.HelmError, "no backlog column"):
            helper.cmd_backlog(StubClient(), args)  # type: ignore[arg-type]

    def test_resume_claims_then_activates_only_when_needed(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []
        current = {"id": "task-1", "key": "TC-1", "project_id": "project-1", "column_id": "ready", "version": 3}
        claimed = {**current, "claimed_by": "agent-1", "version": 4}
        moved = {**claimed, "column_id": "active", "version": 5}

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method == "GET" and path.startswith("/tasks/"):
                    return current, {}
                if path.endswith("/claim"):
                    return claimed, {}
                if path.endswith("/columns?limit=200"):
                    return {"data": [{"id": "active", "semantic_state": "active"}]}, {}
                return moved, {}

        args = argparse.Namespace(task="TC-1", lease_seconds=600, operation_id="resume-1")
        result = helper.cmd_resume(StubClient(), args)  # type: ignore[arg-type]
        self.assertEqual(result["task"], moved)
        self.assertEqual(calls[1][0:2], ("POST", "/tasks/task-1/claim"))
        self.assertEqual(calls[2][0:2], ("GET", "/projects/project-1/columns?limit=200"))
        self.assertEqual(calls[3][0:2], ("PATCH", "/tasks/task-1"))
        self.assertEqual(calls[3][2]["if_match"], 4)
        self.assertNotEqual(calls[1][2]["idempotency_key"], calls[3][2]["idempotency_key"])

    def test_resume_does_not_patch_already_active_task_or_foreign_claim(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []
        current = {"id": "task-1", "project_id": "project-1", "column_id": "active", "version": 3}
        claimed = {**current, "version": 4}

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method == "GET":
                    if path.endswith("/columns?limit=200"):
                        return {"data": [{"id": "active", "semantic_state": "active"}]}, {}
                    return current, {}
                if path.endswith("/claim"):
                    return claimed, {}
                return claimed, {}

        args = argparse.Namespace(task="TC-1", lease_seconds=600, operation_id="resume-2")
        helper.cmd_resume(StubClient(), args)  # type: ignore[arg-type]
        self.assertFalse(any(method == "PATCH" for method, _path, _kwargs in calls))

    def test_operation_id_is_rejected_before_network_mutation(self) -> None:
        class StubClient:
            def call(self, *_args, **_kwargs):  # type: ignore[no-untyped-def]
                self.fail("network should not be called")

        args = argparse.Namespace(task="TC-1", operation_id="bad id", lease_seconds=600)
        with self.assertRaisesRegex(helper.HelmError, "operation_id"):
            helper.cmd_heartbeat(StubClient(), args)  # type: ignore[arg-type]

    def test_heartbeat_updates_state_when_task_is_referenced_by_key(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            store = session.StateStore("session-heartbeat", directory=raw)
            store.save(
                session.SessionState(
                    task_id="task-1",
                    task_key="TC-35",
                    project_id="project-1",
                    operation_id="heartbeat-1",
                    snapshot_ready=True,
                )
            )
            args = argparse.Namespace(task="TC-35", operation_id="heartbeat-1")
            calls: list[tuple[str, str, dict[str, object]]] = []

            class StubClient:
                def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                    calls.append((method, path, kwargs))
                    return {"id": "task-1", "version": 3}, {}

            with mock.patch.dict(os.environ, {"CODEX_SESSION_ID": "session-heartbeat", "HELM_STATE_DIR": raw}, clear=False):
                result = helper.cmd_heartbeat(StubClient(), args)  # type: ignore[arg-type]
            self.assertEqual(calls[0][0:2], ("POST", "/tasks/TC-35/heartbeat"))
            self.assertEqual(calls[0][2]["body"], {"operation_id": "heartbeat-1"})
            self.assertNotIn("if_match", calls[0][2])
            self.assertNotIn("idempotency_key", calls[0][2])
            self.assertEqual(result["operation_id"], "heartbeat-1")
            self.assertIsNotNone(store.load().last_heartbeat_at)  # type: ignore[union-attr]

    def test_audit_parser_supports_default_and_repeatable_semantic_states(self) -> None:
        default = helper.build_parser().parse_args(["audit", "--project", "TC"])
        self.assertIsNone(default.states)
        self.assertEqual(default.page_size, 200)
        self.assertEqual(default.evidence_limit, helper.AUDIT_MAX_EVIDENCE_ITEMS)

        selected = helper.build_parser().parse_args(
            [
                "audit",
                "--project",
                "TC",
                "--state",
                "ready",
                "--semantic-state",
                "blocked",
                "--limit",
                "2",
                "--evidence-limit",
                "1",
            ]
        )
        self.assertEqual(selected.states, ["ready", "blocked"])
        self.assertEqual(selected.page_size, 2)
        self.assertEqual(selected.evidence_limit, 1)

    def test_audit_follows_every_task_page_and_uses_get_only(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []
        backlog_one = {"id": "backlog-1", "key": "TC-1", "column_id": "column-backlog", "version": 3, "title": "One", "description": "Goal: one", "priority": "normal"}
        backlog_two = {"id": "backlog-2", "key": "TC-2", "column_id": "column-backlog", "version": 4, "title": "Two", "description": "Goal: two", "priority": "high"}
        active = {"id": "active-1", "key": "TC-3", "column_id": "column-active", "version": 5, "title": "Three", "description": "Goal: three", "priority": "urgent"}

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if path == "/projects?limit=200":
                    return {"data": [{"id": "project-1", "key": "TC"}]}, {}
                if path == "/projects/project-1/columns?limit=1":
                    return {
                        "data": [
                            {"id": "column-backlog", "project_id": "project-1", "name": "Backlog", "semantic_state": "backlog"},
                            {"id": "column-active", "project_id": "project-1", "name": "In Progress", "semantic_state": "active"},
                        ]
                    }, {}
                if "state=backlog" in path and "cursor=" not in path:
                    return {"data": [backlog_one], "next_cursor": "backlog-page-2"}, {}
                if "state=backlog" in path and "cursor=backlog-page-2" in path:
                    return {"data": [backlog_two], "next_cursor": ""}, {}
                if "state=active" in path:
                    return {"data": [active], "next_cursor": ""}, {}
                if path.startswith("/tasks/") and "/comments" in path:
                    return {"data": [], "next_cursor": ""}, {}
                raise AssertionError(f"unexpected audit path: {path}")

        result = helper.cmd_audit(
            StubClient(), argparse.Namespace(project="TC", states=None, page_size=1, evidence_limit=0)  # type: ignore[arg-type]
        )
        self.assertEqual([item["key"] for item in result["tasks"]], ["TC-1", "TC-2", "TC-3"])
        self.assertEqual(result["states"], ["backlog", "active"])
        self.assertTrue(result["read_only"])
        self.assertTrue(any("cursor=backlog-page-2" in path for _, path, _ in calls))
        self.assertTrue(calls)
        self.assertTrue(all(method == "GET" for method, _path, _kwargs in calls))

    def test_audit_redacts_and_bounds_task_and_comment_evidence(self) -> None:
        calls: list[str] = []
        secret = "super-secret-token-value"
        task = {
            "id": "task-1",
            "key": "TC-1",
            "column_id": "column-active",
            "version": 9,
            "title": "Inspect credentials",
            "description": "Goal: Bearer " + secret + "\n\nAcceptance criteria\n- [ ] password=" + secret + "\n" + ("x" * 5000),
            "priority": "normal",
            "agent_work": {
                "operation_id": "audit-1",
                "actor_id": "agent-1",
                "state": "working",
                "summary": "Authorization: Bearer " + secret,
                "updated_at": "2026-01-01T00:00:00Z",
                "stale": True,
                "checkpoint_refs": ["safe-ref"],
            },
        }
        comments = [
            {"id": f"comment-{index}", "actor_id": "agent-1", "created_at": f"2026-01-01T00:00:{index:02d}Z", "body": secret if index == 6 else f"evidence {index}"}
            for index in range(7)
        ]

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                if method != "GET":
                    raise AssertionError(f"audit used non-GET method: {method}")
                calls.append(path)
                if path == "/projects?limit=200":
                    return {"data": [{"id": "project-1", "key": "TC"}]}, {}
                if path == "/projects/project-1/columns?limit=2":
                    return {"data": [{"id": "column-active", "project_id": "project-1", "name": "In Progress", "semantic_state": "active"}]}, {}
                if "state=backlog" in path:
                    return {"data": [], "next_cursor": ""}, {}
                if "state=active" in path:
                    return {"data": [task], "next_cursor": ""}, {}
                if path.startswith("/tasks/task-1/comments?limit=2") and "cursor=" not in path:
                    return {"data": comments[:2], "next_cursor": "comment-page-2"}, {}
                if "cursor=comment-page-2" in path:
                    return {"data": comments[2:4], "next_cursor": "comment-page-3"}, {}
                if "cursor=comment-page-3" in path:
                    return {"data": comments[4:], "next_cursor": ""}, {}
                raise AssertionError(f"unexpected audit path: {path}")

        result = helper.cmd_audit(
            StubClient(), argparse.Namespace(project="TC", states=None, page_size=2, evidence_limit=2)  # type: ignore[arg-type]
        )
        audited = result["tasks"][0]
        encoded = json.dumps(audited)
        self.assertNotIn(secret, encoded)
        self.assertNotIn("Authorization", encoded)
        self.assertLessEqual(len(audited["evidence"]), helper.AUDIT_MAX_EVIDENCE_ITEMS)
        self.assertLessEqual(len(audited["description"]), helper.AUDIT_MAX_TEXT)
        self.assertLessEqual(len(audited["goal"]), helper.AUDIT_MAX_TEXT)
        self.assertLessEqual(len(audited["acceptance_criteria"]), helper.AUDIT_MAX_EVIDENCE_REFS)
        self.assertEqual(audited["liveness"]["state"], "stale")
        self.assertIn("warning only", " ".join(audited["warnings"]))
        self.assertTrue(calls)

    def test_audit_rubric_states_and_conservative_liveness_verdicts(self) -> None:
        future = "2999-01-01T00:00:00Z"
        tasks = [
            {
                "id": "backlog-claimed",
                "key": "TC-1",
                "column_id": "column-backlog",
                "version": 1,
                "title": "Claimed",
                "description": "Goal: claimed",
                "priority": "high",
                "claimed_by": "agent-1",
                "claim_expires_at": future,
                "agent_work": {"operation_id": "run-1", "actor_id": "agent-1", "state": "working", "summary": "Working", "updated_at": future, "stale": False},
            },
            {
                "id": "active-stale",
                "key": "TC-2",
                "column_id": "column-active",
                "version": 2,
                "title": "Stale",
                "description": "Goal: stale",
                "priority": "normal",
                "agent_work": {"operation_id": "run-2", "actor_id": "agent-2", "state": "working", "summary": "Old", "updated_at": "2026-01-01T00:00:00Z", "stale": True},
            },
            {
                "id": "active-missing",
                "key": "TC-3",
                "column_id": "column-active",
                "version": 3,
                "title": "Missing",
                "description": "Goal: missing",
                "priority": "normal",
            },
        ]

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                if method != "GET":
                    raise AssertionError(f"audit used non-GET method: {method}")
                if path == "/projects?limit=200":
                    return {"data": [{"id": "project-1", "key": "TC"}]}, {}
                if path == "/projects/project-1/columns?limit=200":
                    return {
                        "data": [
                            {"id": "column-backlog", "name": "Backlog", "semantic_state": "backlog"},
                            {"id": "column-active", "name": "In Progress", "semantic_state": "active"},
                        ]
                    }, {}
                if "state=backlog" in path:
                    return {"data": [tasks[0]], "next_cursor": ""}, {}
                if "state=active" in path:
                    return {"data": tasks[1:], "next_cursor": ""}, {}
                raise AssertionError(f"unexpected audit path: {path}")

        result = helper.cmd_audit(
            StubClient(), argparse.Namespace(project="TC", states=None, page_size=200, evidence_limit=0)  # type: ignore[arg-type]
        )
        by_key = {task["key"]: task for task in result["tasks"]}
        self.assertEqual(by_key["TC-1"]["verdict"], "move_proposed")
        self.assertEqual(by_key["TC-1"]["suggested_semantic_state"], "active")
        self.assertEqual(by_key["TC-2"]["verdict"], "needs_attention")
        self.assertEqual(by_key["TC-2"]["suggested_semantic_state"], "active")
        self.assertEqual(by_key["TC-3"]["verdict"], "needs_attention")
        self.assertEqual(by_key["TC-3"]["suggested_semantic_state"], "active")
        self.assertEqual(result["rubric"]["allowed_verdicts"], ["correct", "needs_attention", "move_proposed"])
        self.assertTrue(any("stale or missing" in constraint.lower() for constraint in result["rubric"]["constraints"]))

    def test_submit_creates_appends_and_finalizes_with_stable_idempotency(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []
        findings = [
            {
                "id": "task-1",
                "version": 4,
                "current_column": {"id": "column-backlog", "semantic_state": "backlog"},
                "verdict": "move_proposed",
                "suggested_semantic_state": "ready",
                "confidence": 0.9,
                "reason": "Ready for review",
                "evidence_refs": ["comment:1"],
                "review_state": "approved",
            },
            {
                "id": "task-2",
                "version": 5,
                "current_column": {"id": "column-active", "semantic_state": "active"},
                "verdict": "needs_attention",
                "confidence": 0.6,
                "reason": "Pulse needs review",
            },
        ]

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method == "GET" and path == "/projects?limit=200":
                    return {"data": [{"id": "project-1", "key": "TC"}]}, {}
                if method == "POST" and path == "/projects/project-1/audits":
                    return {"id": "audit-1", "status": "running"}, {}
                if method == "POST" and path == "/audits/audit-1/findings":
                    return {"id": "finding-" + str(len([call for call in calls if call[1] == path]))}, {}
                if method == "POST" and path == "/audits/audit-1/finalize":
                    return {"id": "audit-1", "status": kwargs["body"]["status"]}, {}
                raise AssertionError(f"unexpected submission call: {method} {path}")

        result = helper.cmd_submit(
            StubClient(),
            argparse.Namespace(
                project="TC",
                findings={"tasks": findings},
                scope="board",
                status="complete",
                operation_id="submit-1",
            ),
        )
        self.assertEqual(result["audit_id"], "audit-1")
        self.assertEqual(result["status"], "complete")
        self.assertEqual(result["appended"], 2)
        self.assertEqual([method for method, _path, _kwargs in calls], ["GET", "POST", "POST", "POST", "POST"])
        self.assertEqual(calls[1][2]["body"], {"scope": "board"})
        self.assertEqual(calls[2][2]["body"]["source_column"], "column-backlog")
        self.assertEqual(calls[2][2]["body"]["review_state"], "pending")
        self.assertNotIn("proposed_semantic_destination", calls[3][2]["body"] if isinstance(calls[3][2].get("body"), dict) else {})
        self.assertNotEqual(calls[2][2]["idempotency_key"], calls[3][2]["idempotency_key"])

    def test_submit_processes_the_existing_ui_queued_audit(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method == "GET" and path == "/projects?limit=200":
                    return {"data": [{"id": "project-1", "key": "TC"}]}, {}
                if method == "GET" and path == "/audits/audit-queued":
                    return {"id": "audit-queued", "project_id": "project-1", "status": "queued"}, {}
                if method == "POST" and path == "/audits/audit-queued/findings":
                    return {"id": "finding-1"}, {}
                if method == "POST" and path == "/audits/audit-queued/finalize":
                    return {"id": "audit-queued", "status": "complete"}, {}
                raise AssertionError(f"unexpected submission call: {method} {path}")

        result = helper.cmd_submit(
            StubClient(),
            argparse.Namespace(
                project="TC",
                audit="audit-queued",
                findings={
                    "tasks": [
                        {
                            "id": "task-1",
                            "version": 2,
                            "current_column": {"id": "column-backlog", "semantic_state": "backlog"},
                            "verdict": "correct",
                            "confidence": 0.9,
                            "reason": "Correctly placed",
                        }
                    ]
                },
                scope="board",
                status="complete",
                operation_id="submit-existing-1",
            ),
        )

        self.assertEqual(result["audit_id"], "audit-queued")
        self.assertEqual(result["status"], "complete")
        self.assertEqual([method for method, _path, _kwargs in calls], ["GET", "GET", "POST", "POST"])
        self.assertFalse(any(path == "/projects/project-1/audits" for _method, path, _kwargs in calls))

    def test_submit_rejects_a_queued_audit_from_another_project(self) -> None:
        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                if method == "GET" and path == "/projects?limit=200":
                    return {"data": [{"id": "project-1", "key": "TC"}]}, {}
                if method == "GET" and path == "/audits/audit-other":
                    return {"id": "audit-other", "project_id": "project-2", "status": "queued"}, {}
                raise AssertionError(f"unexpected submission call: {method} {path}")

        with self.assertRaisesRegex(helper.HelmError, "different project"):
            helper.cmd_submit(
                StubClient(),
                argparse.Namespace(
                    project="TC",
                    audit="audit-other",
                    findings={"tasks": []},
                    scope="board",
                    status="complete",
                    operation_id="submit-existing-2",
                ),
            )

    def test_reconcile_preview_fetches_paginated_findings_and_never_moves(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []
        findings = [
            {"id": "finding-ready", "task_id": "task-ready", "captured_version": 7, "source_column": "column-backlog", "verdict": "move_proposed", "proposed_semantic_destination": "ready", "confidence": 0.9, "reason": "ready", "review_state": "approved"},
            {"id": "finding-pending", "task_id": "task-pending", "captured_version": 4, "source_column": "column-backlog", "verdict": "move_proposed", "proposed_semantic_destination": "ready", "confidence": 0.8, "reason": "pending", "review_state": "pending"},
            {"id": "finding-changed", "task_id": "task-changed", "captured_version": 2, "source_column": "column-backlog", "verdict": "move_proposed", "proposed_semantic_destination": "ready", "confidence": 0.8, "reason": "changed", "review_state": "approved"},
            {"id": "finding-active", "task_id": "task-active", "captured_version": 3, "source_column": "column-backlog", "verdict": "move_proposed", "proposed_semantic_destination": "active", "confidence": 0.8, "reason": "claim", "review_state": "approved"},
        ]
        tasks = {
            "task-ready": {"id": "task-ready", "version": 7, "column_id": "column-backlog"},
            "task-pending": {"id": "task-pending", "version": 4, "column_id": "column-backlog"},
            "task-changed": {"id": "task-changed", "version": 3, "column_id": "column-backlog"},
            "task-active": {"id": "task-active", "version": 3, "column_id": "column-backlog", "claimed_by": "agent-1", "claim_expires_at": "2999-01-01T00:00:00Z"},
        }

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method != "GET":
                    raise AssertionError(f"preview used non-GET method: {method}")
                if path == "/audits/audit-1":
                    return {"id": "audit-1", "project_id": "project-1", "status": "complete"}, {}
                if path == "/audits/audit-1/findings?limit=2":
                    return {"data": findings[:2], "next_cursor": "findings-2"}, {}
                if "findings-2" in path:
                    return {"data": findings[2:], "next_cursor": ""}, {}
                if path == "/projects/project-1/columns?limit=2":
                    return {
                        "data": [
                            {"id": "column-backlog", "name": "Backlog", "semantic_state": "backlog"},
                            {"id": "column-ready", "name": "Ready", "semantic_state": "ready"},
                            {"id": "column-active", "name": "In Progress", "semantic_state": "active"},
                        ]
                    }, {}
                if path.startswith("/tasks/"):
                    return tasks[path.removeprefix("/tasks/")], {}
                raise AssertionError(f"unexpected preview call: {path}")

        result = helper.cmd_reconcile(
            StubClient(), argparse.Namespace(audit="audit-1", apply=False, page_size=2, operation_id=None)  # type: ignore[arg-type]
        )
        by_id = {item["finding_id"]: item for item in result["findings"]}
        self.assertEqual(by_id["finding-ready"]["status"], "preview")
        self.assertEqual(by_id["finding-pending"]["action_required"], "review_finding")
        self.assertEqual(by_id["finding-changed"]["action_required"], "rerun_audit")
        self.assertEqual(by_id["finding-active"]["action_required"], "claim_or_resume")
        self.assertEqual(result["summary"]["preview"], 1)
        self.assertTrue(result["read_only"])
        self.assertTrue(any("cursor=findings-2" in path for _method, path, _kwargs in calls))
        self.assertTrue(all(method == "GET" for method, _path, _kwargs in calls))

    def test_reconcile_apply_uses_guarded_move_and_deterministic_key(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if path == "/audits/audit-1":
                    return {"id": "audit-1", "project_id": "project-1", "status": "complete"}, {}
                if path == "/audits/audit-1/findings?limit=200":
                    return {"data": [{"id": "finding-1", "task_id": "task-1", "captured_version": 7, "source_column": "column-backlog", "verdict": "move_proposed", "proposed_semantic_destination": "ready", "confidence": 0.9, "reason": "approved", "review_state": "approved"}], "next_cursor": ""}, {}
                if path == "/projects/project-1/columns?limit=200":
                    return {"data": [{"id": "column-backlog", "semantic_state": "backlog"}, {"id": "column-ready", "semantic_state": "ready"}]}, {}
                if path == "/tasks/task-1":
                    return {"id": "task-1", "version": 7, "column_id": "column-backlog"}, {}
                if path == "/tasks/task-1/move":
                    return {"id": "task-1", "version": 8, "column_id": "column-ready"}, {}
                raise AssertionError(f"unexpected apply call: {method} {path}")

        result = helper.cmd_reconcile(
            StubClient(), argparse.Namespace(audit="audit-1", apply=True, page_size=200, operation_id="reconcile-1")  # type: ignore[arg-type]
        )
        move = next((entry for entry in calls if entry[1] == "/tasks/task-1/move"), None)
        self.assertIsNotNone(move)
        self.assertEqual(move[0], "POST")  # type: ignore[union-attr]
        self.assertEqual(move[2]["if_match"], 7)  # type: ignore[union-attr]
        self.assertEqual(move[2]["body"], {  # type: ignore[union-attr]
            "destination_column_id": "column-ready",
            "expected_source_column_id": "column-backlog",
            "source": "board_audit",
            "reason": "approved",
        })
        self.assertEqual(result["summary"]["applied"], 1)
        first_key = move[2]["idempotency_key"]  # type: ignore[union-attr]
        calls.clear()
        second = helper.cmd_reconcile(
            StubClient(), argparse.Namespace(audit="audit-1", apply=True, page_size=200, operation_id="reconcile-1")  # type: ignore[arg-type]
        )
        second_move = next(entry for entry in calls if entry[1] == "/tasks/task-1/move")
        self.assertEqual(second_move[2]["idempotency_key"], first_key)
        self.assertEqual(second["summary"]["applied"], 1)

    def test_reconcile_never_previews_or_applies_an_unfinished_audit(self) -> None:
        methods_and_paths: list[tuple[str, str]] = []

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                methods_and_paths.append((method, path))
                if path == "/audits/audit-running":
                    return {"id": "audit-running", "project_id": "project-1", "status": "running"}, {}
                if path == "/audits/audit-running/findings?limit=200":
                    return {"data": [{"id": "finding-1", "task_id": "task-1", "captured_version": 1, "source_column": "column-backlog", "verdict": "move_proposed", "proposed_semantic_destination": "ready", "review_state": "approved"}], "next_cursor": ""}, {}
                if path == "/projects/project-1/columns?limit=200":
                    return {"data": [{"id": "column-backlog", "semantic_state": "backlog"}, {"id": "column-ready", "semantic_state": "ready"}]}, {}
                raise AssertionError(f"unfinished audit touched task or mutation route: {method} {path}")

        result = helper.cmd_reconcile(
            StubClient(), argparse.Namespace(audit="audit-running", apply=True, page_size=200, operation_id="reconcile-running")  # type: ignore[arg-type]
        )
        self.assertEqual(result["audit_status"], "running")
        self.assertEqual(result["summary"], {"total": 1, "preview": 0, "applied": 0, "skipped": 1, "conflicted": 0, "errors": 0})
        self.assertEqual(result["findings"][0]["action_required"], "wait_for_audit")
        self.assertTrue(all(method == "GET" for method, _path in methods_and_paths))

    def test_reconcile_partial_batch_reports_conflicts_and_skips_completed_retries(self) -> None:
        moved = False
        move_calls: list[str] = []
        findings = [
            {"id": "finding-applied", "task_id": "task-applied", "captured_version": 1, "source_column": "column-backlog", "verdict": "move_proposed", "proposed_semantic_destination": "ready", "confidence": 0.9, "reason": "apply", "review_state": "approved"},
            {"id": "finding-pending", "task_id": "task-pending", "captured_version": 1, "source_column": "column-backlog", "verdict": "move_proposed", "proposed_semantic_destination": "ready", "confidence": 0.9, "reason": "wait", "review_state": "pending"},
            {"id": "finding-conflict", "task_id": "task-conflict", "captured_version": 1, "source_column": "column-backlog", "verdict": "move_proposed", "proposed_semantic_destination": "ready", "confidence": 0.9, "reason": "race", "review_state": "approved"},
        ]

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                nonlocal moved
                if path == "/audits/audit-batch":
                    return {"id": "audit-batch", "project_id": "project-1", "status": "complete"}, {}
                if path == "/audits/audit-batch/findings?limit=200":
                    return {"data": findings, "next_cursor": ""}, {}
                if path == "/projects/project-1/columns?limit=200":
                    return {"data": [{"id": "column-backlog", "semantic_state": "backlog"}, {"id": "column-ready", "semantic_state": "ready"}], "next_cursor": ""}, {}
                if path == "/tasks/task-applied":
                    if moved:
                        return {"id": "task-applied", "project_id": "project-1", "version": 2, "column_id": "column-ready"}, {}
                    return {"id": "task-applied", "project_id": "project-1", "version": 1, "column_id": "column-backlog"}, {}
                if path in {"/tasks/task-pending", "/tasks/task-conflict"}:
                    return {"id": path.rsplit("/", 1)[-1], "project_id": "project-1", "version": 1, "column_id": "column-backlog"}, {}
                if path == "/tasks/task-applied/move":
                    move_calls.append(path)
                    moved = True
                    return {"id": "task-applied", "version": 2, "column_id": "column-ready"}, {}
                if path == "/tasks/task-conflict/move":
                    move_calls.append(path)
                    raise helper.HelmError("conflict", status_code=409, error_code="stale_task")
                raise AssertionError(f"unexpected batch call: {method} {path}")

        first = helper.cmd_reconcile(
            StubClient(), argparse.Namespace(audit="audit-batch", apply=True, page_size=200, operation_id="reconcile-batch")  # type: ignore[arg-type]
        )
        self.assertEqual(first["summary"], {"total": 3, "preview": 0, "applied": 1, "skipped": 1, "conflicted": 1, "errors": 0})
        by_id = {item["finding_id"]: item for item in first["findings"]}
        self.assertEqual(by_id["finding-conflict"]["error"]["code"], "stale_task")

        move_calls.clear()
        second = helper.cmd_reconcile(
            StubClient(), argparse.Namespace(audit="audit-batch", apply=True, page_size=200, operation_id="reconcile-batch")  # type: ignore[arg-type]
        )
        self.assertNotIn("/tasks/task-applied/move", move_calls)
        self.assertEqual(second["summary"]["applied"], 0)
        self.assertEqual(second["summary"]["conflicted"], 1)


class WorkflowCommandTests(unittest.TestCase):
    def test_auth_check_sanitizes_identity_fields(self) -> None:
        calls: list[tuple[str, str]] = []

        class StubClient:
            def call(self, method: str, path: str, **_kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path))
                return {
                    "id": "agent-1",
                    "kind": "agent",
                    "name": "Build agent",
                    "admin": False,
                    "email": "private@example.invalid",
                    "token": "do-not-return",
                    "project_ids": ["project-1"],
                    "scopes": ["tasks:read"],
                }, {}

        result = helper.cmd_auth_check(StubClient(), argparse.Namespace())  # type: ignore[arg-type]
        self.assertEqual(calls, [("GET", "/auth/me")])
        self.assertTrue(result["ok"])
        self.assertNotIn("email", result["actor"])
        self.assertNotIn("token", json.dumps(result))
        self.assertEqual(result["actor"]["project_ids"], ["project-1"])

    def test_auth_check_returns_actionable_sanitized_failure(self) -> None:
        class StubClient:
            config = helper.Config("https://tc.example", "secret-token")

            def call(self, *_args, **_kwargs):  # type: ignore[no-untyped-def]
                raise helper.HelmError("authentication required: secret-token", status_code=401, error_code="unauthorized")

        result = helper.cmd_auth_check(StubClient(), argparse.Namespace())  # type: ignore[arg-type]
        self.assertFalse(result["ok"])
        self.assertEqual(result["error"]["code"], "unauthorized")
        self.assertIn("HELM_TOKEN", result["error"]["hint"])
        self.assertNotIn("secret-token", json.dumps(result))

    def test_new_operation_id_is_uuid4_and_rejects_malformed_explicit_ids(self) -> None:
        generated = helper._new_operation_id()
        self.assertEqual(generated, str(uuid.UUID(generated)))
        self.assertEqual(uuid.UUID(generated).version, 4)
        with self.assertRaisesRegex(helper.HelmError, "UUIDv4"):
            helper._new_operation_id("legacy-operation")

    def test_uuid_operation_derives_distinct_replay_safe_keys_per_mutation(self) -> None:
        operation_id = "11111111-1111-4111-8111-111111111111"
        first = helper._command_mutation_id(operation_id, "POST", "/tasks/a", {"x": 1})
        second = helper._command_mutation_id(operation_id, "POST", "/tasks/b", {"x": 1})
        self.assertNotEqual(first, second)
        self.assertEqual(first, helper._command_mutation_id(operation_id, "POST", "/tasks/a", {"x": 1}))
        self.assertEqual(uuid.UUID(first).version, 4)
        self.assertEqual(uuid.UUID(second).version, 4)

    def test_workflow_parser_dispatches_renew_and_release(self) -> None:
        operation_id = "11111111-1111-4111-8111-111111111111"
        renew = helper.build_parser().parse_args(
            [
                "renew",
                "--task",
                "TC-1",
                "--lease-seconds",
                "600",
                "--operation-id",
                operation_id,
            ]
        )
        self.assertEqual(renew.command, "renew")
        self.assertIs(renew.handler, helper.cmd_renew)
        self.assertEqual(renew.task, "TC-1")
        self.assertEqual(renew.lease_seconds, 600)
        self.assertEqual(renew.operation_id, operation_id)

        release = helper.build_parser().parse_args(
            ["release", "--task", "TC-1", "--operation-id", operation_id]
        )
        self.assertEqual(release.command, "release")
        self.assertIs(release.handler, helper.cmd_release)
        self.assertEqual(release.task, "TC-1")
        self.assertEqual(release.operation_id, operation_id)

    def test_renew_uses_explicit_route_etag_and_uuid_key(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []
        fixed = "11111111-1111-4111-8111-111111111111"

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method == "GET":
                    return {"id": "task-1", "version": 3, "claimed_by": "agent-1"}, {}
                return {"id": "task-1", "version": 4}, {}

        with mock.patch.object(helper.uuid, "uuid4", return_value=uuid.UUID(fixed)):
            result = helper.cmd_renew(
                StubClient(), argparse.Namespace(task="TC-1", lease_seconds=600, operation_id=None)  # type: ignore[arg-type]
            )
        self.assertEqual(calls[1][0:2], ("POST", "/tasks/task-1/renew"))
        self.assertEqual(calls[1][2]["if_match"], 3)
        self.assertEqual(calls[1][2]["idempotency_key"], fixed)
        self.assertEqual(result["operation_id"], fixed)

    def test_dependency_add_and_remove_use_task_version_and_uuid_key(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []
        fixed = "22222222-2222-4222-8222-222222222222"

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method == "GET":
                    return {"id": "task-1", "key": "TC-1", "version": 7}, {}
                return {"id": "task-1", "version": 8}, {}

        add_args = argparse.Namespace(task="TC-1", prerequisite="TC-2", operation_id=None)
        remove_args = argparse.Namespace(task="TC-1", prerequisite="TC-2", operation_id=None)
        with mock.patch.object(helper.uuid, "uuid4", return_value=uuid.UUID(fixed)):
            helper.cmd_dependency_add(StubClient(), add_args)  # type: ignore[arg-type]
            helper.cmd_dependency_remove(StubClient(), remove_args)  # type: ignore[arg-type]
        self.assertEqual(calls[1][0:2], ("POST", "/tasks/task-1/dependencies"))
        self.assertEqual(calls[1][2]["body"], {"prerequisite": "TC-2"})
        self.assertEqual(calls[1][2]["if_match"], 7)
        self.assertEqual(calls[1][2]["idempotency_key"], fixed)
        self.assertEqual(calls[3][0:2], ("DELETE", "/tasks/task-1/dependencies/TC-2"))
        self.assertEqual(calls[3][2]["if_match"], 7)
        self.assertEqual(calls[3][2]["idempotency_key"], fixed)

    def test_timeline_preserves_opaque_before_cursor(self) -> None:
        paths: list[str] = []

        class StubClient:
            def call(self, _method: str, path: str, **_kwargs):  # type: ignore[no-untyped-def]
                paths.append(path)
                if "before=" not in path:
                    return {"data": [{"id": "event-1"}], "next_cursor": "opaque/cursor"}, {}
                return {"data": [{"id": "event-2"}], "next_cursor": ""}, {}

        result = helper.cmd_timeline(
            StubClient(),
            argparse.Namespace(task="TC-1", project=None, before="", kind="comment", limit=1, all=True),  # type: ignore[arg-type]
        )
        self.assertEqual([item["id"] for item in result["data"]], ["event-1", "event-2"])
        self.assertIn("before=opaque%2Fcursor", paths[1])
        self.assertEqual(result["next_cursor"], "")

    def test_events_follows_monotonic_cursor(self) -> None:
        paths: list[str] = []

        class StubClient:
            def call(self, _method: str, path: str, **_kwargs):  # type: ignore[no-untyped-def]
                paths.append(path)
                if "after=0" in path:
                    return {"data": [{"cursor": 1}], "next_cursor": "2"}, {}
                return {"data": [{"cursor": 3}], "next_cursor": ""}, {}

        result = helper.cmd_events(
            StubClient(), argparse.Namespace(after=0, project="TC", limit=1, all=True)  # type: ignore[arg-type]
        )
        self.assertEqual(len(result["data"]), 2)
        self.assertIn("after=2", paths[1])

    def test_events_rejects_repeated_cursor_without_looping(self) -> None:
        class StubClient:
            def call(self, _method: str, _path: str, **_kwargs):  # type: ignore[no-untyped-def]
                return {"data": [{"cursor": 1}], "next_cursor": "1"}, {}

        with self.assertRaisesRegex(helper.HelmError, "repeated cursor"):
            helper.cmd_events(StubClient(), argparse.Namespace(after=0, project=None, limit=1, all=True))  # type: ignore[arg-type]

    def test_timeline_rejects_repeated_opaque_cursor_without_looping(self) -> None:
        class StubClient:
            def call(self, _method: str, _path: str, **_kwargs):  # type: ignore[no-untyped-def]
                return {"data": [{"id": "event-1"}], "next_cursor": "same"}, {}

        with self.assertRaisesRegex(helper.HelmError, "repeated cursor"):
            helper.cmd_timeline(
                StubClient(),
                argparse.Namespace(task="TC-1", project=None, before="", kind=None, limit=1, all=True),  # type: ignore[arg-type]
            )

    def test_issues_support_filters_and_cursor_pages(self) -> None:
        paths: list[str] = []

        class StubClient:
            def call(self, _method: str, path: str, **_kwargs):  # type: ignore[no-untyped-def]
                paths.append(path)
                if "cursor=opaque" not in path:
                    return {"data": [{"id": "bug-1"}], "next_cursor": "opaque"}, {}
                return {"data": [{"id": "bug-2"}], "next_cursor": ""}, {}

        result = helper.cmd_issues(
            StubClient(),
            argparse.Namespace(
                project="TC",
                state=None,
                column=None,
                priority="high",
                severity="untriaged",
                label=None,
                assignee=None,
                reporter=None,
                resolution="unresolved",
                agent_state=None,
                action_needed="true",
                query="crash",
                updated_after=None,
                cursor="",
                limit=1,
                all=True,
            ),  # type: ignore[arg-type]
        )
        self.assertEqual([item["id"] for item in result["data"]], ["bug-1", "bug-2"])
        self.assertIn("severity=untriaged", paths[0])
        self.assertIn("cursor=opaque", paths[1])

    def test_bug_report_and_lifecycle_actions_send_structured_payloads(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []
        fixed = "33333333-3333-4333-8333-333333333333"

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method == "GET" and path == "/projects?limit=200":
                    return {"data": [{"id": "project-1", "key": "TC"}]}, {}
                if method == "GET":
                    return {"id": "bug-1", "version": 2, "kind": "bug"}, {}
                return {"id": "bug-1", "version": 3}, {}

        report_args = argparse.Namespace(
            project="TC",
            title="Crash",
            actual_behavior="It exits",
            expected_behavior="It completes",
            reproduction_steps=None,
            environment=None,
            affected_version=None,
            severity="s2",
            description=None,
            priority="high",
            column=None,
            assignee=None,
            operation_id=None,
        )
        triage_args = argparse.Namespace(task="TC-1", severity="s2", priority=None, assignee=None, column=None, operation_id=None)
        resolve_args = argparse.Namespace(task="TC-1", resolution="fixed", duplicate_of=None, note=None, operation_id=None)
        with mock.patch.object(helper.uuid, "uuid4", return_value=uuid.UUID(fixed)):
            helper.cmd_bug_report(StubClient(), report_args)  # type: ignore[arg-type]
            helper.cmd_bug_triage(StubClient(), triage_args)  # type: ignore[arg-type]
            helper.cmd_bug_resolve(StubClient(), resolve_args)  # type: ignore[arg-type]
        report = next(entry for entry in calls if entry[1] == "/projects/project-1/tasks")
        self.assertEqual(report[2]["body"]["kind"], "bug")
        self.assertEqual(report[2]["body"]["bug"]["actual_behavior"], "It exits")
        self.assertEqual(report[2]["idempotency_key"], fixed)
        triage = next(entry for entry in calls if entry[1] == "/tasks/bug-1/triage")
        self.assertEqual(triage[2]["if_match"], 2)
        self.assertEqual(triage[2]["body"], {"severity": "s2"})
        resolve = next(entry for entry in calls if entry[1] == "/tasks/bug-1/resolve")
        self.assertEqual(resolve[2]["body"], {"resolution": "fixed"})

    def test_bug_duplicate_and_reopen_send_structured_payloads(self) -> None:
        calls: list[tuple[str, str, dict[str, object]]] = []
        fixed = "44444444-4444-4444-8444-444444444444"

        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                calls.append((method, path, kwargs))
                if method == "GET":
                    return {"id": "bug-1", "version": 8, "kind": "bug"}, {}
                return {"id": "bug-1", "version": 9, "kind": "bug"}, {}

        duplicate_args = argparse.Namespace(
            task="TC-2",
            duplicate_of="TC-3",
            note="Same regression",
            operation_id=None,
        )
        reopen_args = argparse.Namespace(
            task="TC-2",
            reason="The regression remains reproducible",
            operation_id=None,
        )
        with mock.patch.object(helper.uuid, "uuid4", return_value=uuid.UUID(fixed)):
            duplicate = helper.cmd_bug_duplicate(StubClient(), duplicate_args)  # type: ignore[arg-type]
            reopen = helper.cmd_bug_reopen(StubClient(), reopen_args)  # type: ignore[arg-type]

        self.assertEqual(duplicate["operation_id"], fixed)
        self.assertEqual(reopen["operation_id"], fixed)
        self.assertEqual(calls[1][0:2], ("POST", "/tasks/bug-1/resolve"))
        self.assertEqual(
            calls[1][2]["body"],
            {"resolution": "duplicate", "duplicate_of": "TC-3", "note": "Same regression"},
        )
        self.assertEqual(calls[1][2]["if_match"], 8)
        self.assertEqual(calls[3][0:2], ("POST", "/tasks/bug-1/reopen"))
        self.assertEqual(calls[3][2]["body"], {"reason": "The regression remains reproducible"})
        self.assertEqual(calls[3][2]["if_match"], 8)

    def test_cli_parser_dispatches_coherent_agent_workflow(self) -> None:
        """Exercise representative reads and mutations through the real CLI."""

        class WorkflowClient:
            config = helper.Config("https://tc.example", "test-token")

            def __init__(self) -> None:
                self.calls: list[tuple[str, str, dict[str, object]]] = []
                self.task_version = 1
                self.task_claimed = False
                self.task_semantic = "ready"
                self.bug_version = 1

            def task(self) -> dict[str, object]:
                task: dict[str, object] = {
                    "id": "task-1",
                    "key": "TC-1",
                    "project_id": "project-1",
                    "version": self.task_version,
                    "column_id": "column-active" if self.task_semantic == "active" else "column-ready",
                    "semantic_state": self.task_semantic,
                }
                if self.task_claimed:
                    task["claimed_by"] = "agent-1"
                return task

            def bug(self) -> dict[str, object]:
                return {
                    "id": "bug-1",
                    "key": "TC-2",
                    "project_id": "project-1",
                    "version": self.bug_version,
                    "kind": "bug",
                }

            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                self.calls.append((method, path, kwargs))
                if method == "GET" and path == "/auth/me":
                    return {"id": "agent-1", "kind": "agent", "name": "Workflow agent", "admin": False}, {}
                if method == "GET" and path.startswith("/projects?"):
                    return {"data": [{"id": "project-1", "key": "TC", "name": "Test Coordination"}], "next_cursor": ""}, {}
                if method == "GET" and path.startswith("/projects/project-1/tasks?"):
                    return {"data": [self.task()], "next_cursor": ""}, {}
                if method == "GET" and path == "/tasks/TC-1":
                    return self.task(), {}
                if method == "GET" and path == "/tasks/TC-2":
                    return self.bug(), {}
                if method == "GET" and path == "/projects/project-1/columns?limit=200":
                    return {"data": [{"id": "column-active", "semantic_state": "active"}], "next_cursor": ""}, {}
                if method == "GET" and path == "/tasks/task-1/dependencies":
                    return {"prerequisites": [], "dependents": []}, {}
                if method == "GET" and path.startswith("/tasks/TC-1/timeline?"):
                    return {"data": [{"id": "timeline-1", "kind": "agent_progress"}], "next_cursor": ""}, {}
                if method == "GET" and path.startswith("/issues?"):
                    return {"data": [self.bug()], "next_cursor": ""}, {}
                if method == "POST" and path == "/tasks/task-1/claim":
                    self.task_claimed = True
                    self.task_version += 1
                    return self.task(), {}
                if method == "POST" and path == "/tasks/task-1/renew":
                    self.task_version += 1
                    return self.task(), {}
                if method == "POST" and path == "/tasks/task-1/progress":
                    self.task_version += 1
                    return self.task(), {}
                if method == "POST" and path == "/tasks/task-1/release":
                    self.task_claimed = False
                    self.task_version += 1
                    return self.task(), {}
                if method == "PATCH" and path == "/tasks/task-1":
                    self.task_semantic = "active"
                    self.task_version += 1
                    return self.task(), {}
                if method == "POST" and path == "/tasks/task-1/dependencies":
                    self.task_version += 1
                    return self.task(), {}
                if method == "DELETE" and path == "/tasks/task-1/dependencies/TC-2":
                    self.task_version += 1
                    return self.task(), {}
                if method == "POST" and path == "/projects/project-1/tasks":
                    return self.bug(), {}
                if method == "POST" and path in {
                    "/tasks/bug-1/triage",
                    "/tasks/bug-1/resolve",
                    "/tasks/bug-1/reopen",
                }:
                    self.bug_version += 1
                    return self.bug(), {}
                raise AssertionError(f"unexpected mocked API call: {method} {path}")

        client = WorkflowClient()

        def invoke(argv: list[str]) -> object:
            stdout = io.StringIO()
            with (
                mock.patch.object(helper, "Client", return_value=client),
                mock.patch.object(helper, "load_config", return_value=client.config),
                mock.patch.object(helper.sys, "argv", ["helm.py", *argv]),
                mock.patch.object(helper, "_record_session_start"),
                mock.patch.object(helper, "_record_session_progress"),
                mock.patch.object(helper, "_clear_matching_session"),
                contextlib.redirect_stdout(stdout),
            ):
                self.assertEqual(helper.main(), 0, argv)
            return json.loads(stdout.getvalue())

        self.assertTrue(invoke(["auth-check"])["authenticated"])
        self.assertEqual(invoke(["projects", "--limit", "2"])["projects"][0]["key"], "TC")
        self.assertEqual(invoke(["tasks", "--project", "TC", "--limit", "2"])["tasks"][0]["key"], "TC-1")
        self.assertEqual(invoke(["resume", "--task", "TC-1", "--lease-seconds", "600", "--operation-id", "11111111-1111-4111-8111-111111111111"])["task"]["semantic_state"], "active")
        self.assertEqual(invoke(["renew", "--task", "TC-1", "--lease-seconds", "600", "--operation-id", "22222222-2222-4222-8222-222222222222"])["task"]["version"], 4)
        self.assertEqual(invoke(["progress", "--task", "TC-1", "--message", "Working", "--state", "verifying", "--phase", "API", "--completed", "1", "--total", "2", "--next", "Run tests", "--checkpoint-ref", "client", "--checkpoint-ref", "tests", "--operation-id", "33333333-3333-4333-8333-333333333333"])["task"]["version"], 5)
        self.assertFalse(invoke(["release", "--task", "TC-1", "--operation-id", "44444444-4444-4444-8444-444444444444"])["task"].get("claimed_by"))
        self.assertEqual(invoke(["dependencies", "list", "--task", "TC-1"])["prerequisites"], [])
        invoke(["dependencies", "add", "--task", "TC-1", "--prerequisite", "TC-2", "--operation-id", "55555555-5555-4555-8555-555555555555"])
        invoke(["dependencies", "remove", "--task", "TC-1", "--prerequisite", "TC-2", "--operation-id", "66666666-6666-4666-8666-666666666666"])
        self.assertEqual(invoke(["timeline", "--task", "TC-1", "--kind", "agent_progress"])["data"][0]["kind"], "agent_progress")
        self.assertEqual(invoke(["issues", "--project", "TC", "--severity", "s2", "--resolution", "unresolved"])["data"][0]["key"], "TC-2")
        self.assertEqual(invoke(["bug-report", "--project", "TC", "--title", "Crash", "--actual-behavior", "It exits"])["task"]["kind"], "bug")
        invoke(["bug-triage", "--task", "TC-2", "--severity", "s2", "--operation-id", "77777777-7777-4777-8777-777777777777"])
        invoke(["bug-resolve", "--task", "TC-2", "--resolution", "fixed", "--operation-id", "88888888-8888-4888-8888-888888888888"])
        invoke(["bug-duplicate", "--task", "TC-2", "--duplicate-of", "TC-3", "--operation-id", "99999999-9999-4999-8999-999999999999"])
        invoke(["bug-reopen", "--task", "TC-2", "--reason", "Still reproducible", "--operation-id", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"])

        expected_paths = {
            "/auth/me",
            "/projects?limit=2",
            "/projects?limit=200",
            "/projects/project-1/tasks?limit=2",
            "/tasks/TC-1",
            "/tasks/task-1/claim",
            "/tasks/task-1/renew",
            "/tasks/task-1/progress",
            "/tasks/task-1/release",
            "/tasks/task-1/dependencies",
            "/tasks/task-1/dependencies/TC-2",
            "/tasks/TC-1/timeline?kind=agent_progress&limit=50",
            "/issues?project=TC&severity=s2&resolution=unresolved&limit=50",
            "/projects/project-1/tasks",
            "/tasks/bug-1/triage",
            "/tasks/bug-1/resolve",
            "/tasks/bug-1/reopen",
        }
        actual_paths = {path for _method, path, _kwargs in client.calls}
        self.assertTrue(expected_paths.issubset(actual_paths))
        release_call = next(entry for entry in client.calls if entry[1] == "/tasks/task-1/release")
        self.assertNotIn("body", release_call[2])
        for method, path, kwargs in client.calls:
            if method in {"POST", "PATCH", "DELETE"}:
                self.assertIn("idempotency_key", kwargs, (method, path))
                if path != "/projects/project-1/tasks":
                    self.assertIn("if_match", kwargs, (method, path))

    def test_client_request_does_not_place_token_in_url_or_user_agent(self) -> None:
        class Response:
            headers = Message()

            def __enter__(self):  # type: ignore[no-untyped-def]
                return self

            def __exit__(self, *_args):  # type: ignore[no-untyped-def]
                return False

            def read(self, _limit: int) -> bytes:
                return b"{}"

        client = helper.Client(helper.Config("https://tc.example", "secret-token"))
        opener = mock.Mock()
        opener.open.return_value = Response()
        with mock.patch.object(helper.request, "build_opener", return_value=opener):
            client.call("GET", "/auth/me")
        request_object = opener.open.call_args.args[0]
        self.assertNotIn("secret-token", request_object.full_url)
        self.assertNotIn("secret-token", request_object.headers.get("User-agent", ""))
        self.assertEqual(request_object.headers.get("Authorization"), "Bearer secret-token")

    def test_main_emits_machine_readable_sanitized_errors(self) -> None:
        stderr = io.StringIO()
        with mock.patch.object(helper, "load_config", side_effect=helper.HelmError("bad credentials", status_code=401, error_code="unauthorized")), mock.patch.object(
            helper.sys, "argv", ["helm.py", "events"]
        ), contextlib.redirect_stderr(stderr):
            result = helper.main()
        self.assertEqual(result, 1)
        payload = json.loads(stderr.getvalue())
        self.assertEqual(payload["error"]["code"], "unauthorized")
        self.assertNotIn("secret-token", stderr.getvalue())


class RetryTests(unittest.TestCase):
    class Response:
        def __init__(self, body: bytes = b"{}", headers: Message | None = None) -> None:
            self.body = body
            self.headers = headers or Message()

        def __enter__(self):  # type: ignore[no-untyped-def]
            return self

        def __exit__(self, *_args):  # type: ignore[no-untyped-def]
            return False

        def read(self, _limit: int) -> bytes:
            return self.body

    @staticmethod
    def _http_error(status: int, body: bytes = b'{"error":{"code":"busy","message":"try later"}}', retry_after: str = "") -> helper.error.HTTPError:
        headers = Message()
        if retry_after:
            headers["Retry-After"] = retry_after
        return helper.error.HTTPError("https://tc.example/api/v1/tasks", status, "busy", headers, io.BytesIO(body))

    def test_get_and_replay_safe_mutation_retry_twice_with_bounded_delay(self) -> None:
        client = helper.Client(helper.Config("https://tc.example", "token"))
        opener = mock.Mock()
        opener.open.side_effect = [self._http_error(503, retry_after="99"), self._http_error(429, retry_after="1"), self.Response()]
        with mock.patch.object(helper.request, "build_opener", return_value=opener), mock.patch.object(helper.time, "sleep") as sleep:
            payload, _ = client.call("POST", "/tasks/task-1/claim", body={"lease_seconds": 600}, idempotency_key="deterministic")
        self.assertEqual(payload, {})
        self.assertEqual(opener.open.call_count, 3)
        self.assertEqual([call.args[0] for call in sleep.call_args_list], [5.0, 1.0])

    def test_non_replayable_mutation_is_not_retried_and_error_context_survives(self) -> None:
        client = helper.Client(helper.Config("https://tc.example", "token"))
        opener = mock.Mock()
        opener.open.side_effect = [self._http_error(503, retry_after="4")]
        with mock.patch.object(helper.request, "build_opener", return_value=opener), mock.patch.object(helper.time, "sleep") as sleep:
            with self.assertRaises(helper.HelmError) as caught:
                client.call("POST", "/tasks/task-1/comments", body={"body": "note"})
        self.assertEqual(opener.open.call_count, 1)
        sleep.assert_not_called()
        self.assertEqual(caught.exception.status_code, 503)
        self.assertEqual(caught.exception.code, 503)
        self.assertEqual(caught.exception.error_code, "busy")
        self.assertEqual(caught.exception.retry_after, "4")

    def test_heartbeat_without_key_is_replay_safe_but_conflict_is_not_retried(self) -> None:
        client = helper.Client(helper.Config("https://tc.example", "token"))
        opener = mock.Mock()
        opener.open.side_effect = [self._http_error(503), self.Response()]
        with mock.patch.object(helper.request, "build_opener", return_value=opener), mock.patch.object(helper.time, "sleep"):
            payload, _ = client.call("POST", "/tasks/task-1/heartbeat", body={"operation_id": "op-1"})
        self.assertEqual(payload, {})
        self.assertEqual(opener.open.call_count, 2)

        opener.open.reset_mock()
        opener.open.side_effect = [self._http_error(409)]
        with mock.patch.object(helper.request, "build_opener", return_value=opener), mock.patch.object(helper.time, "sleep") as sleep:
            with self.assertRaises(helper.HelmError):
                client.call("GET", "/tasks/task-1")
        self.assertEqual(opener.open.call_count, 1)
        sleep.assert_not_called()


if __name__ == "__main__":
    unittest.main(verbosity=2)
