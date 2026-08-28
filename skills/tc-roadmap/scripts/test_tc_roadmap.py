#!/usr/bin/env python3
"""Unit tests for the TC Roadmap helper."""

from __future__ import annotations

import argparse
import io
import json
import os
import tempfile
import unittest
from email.message import Message
from pathlib import Path
from unittest import mock

import tc_roadmap as helper
import roadmap_session as session


class ConfigTests(unittest.TestCase):
    def test_loads_mode_0600_config_without_exposing_values(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / "credentials.json"
            path.write_text(
                json.dumps({"base_url": "https://tc.example", "token": "private-token"}),
                encoding="utf-8",
            )
            path.chmod(0o600)
            with mock.patch.dict(os.environ, {"TC_ROADMAP_CONFIG": str(path)}, clear=True):
                config = helper.load_config()
            self.assertEqual(config.base_url, "https://tc.example")
            self.assertEqual(config.token, "private-token")

    def test_rejects_broad_credential_permissions(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / "credentials.json"
            path.write_text('{"token":"private-token"}', encoding="utf-8")
            path.chmod(0o644)
            with mock.patch.dict(os.environ, {"TC_ROADMAP_CONFIG": str(path)}, clear=True):
                with self.assertRaisesRegex(helper.RoadmapError, "permissions are too broad"):
                    helper.load_config()

    def test_rejects_remote_plain_http(self) -> None:
        with mock.patch.dict(
            os.environ,
            {"TC_ROADMAP_URL": "http://tc.example", "TC_ROADMAP_TOKEN": "private-token"},
            clear=True,
        ):
            with self.assertRaisesRegex(helper.RoadmapError, "must use HTTPS"):
                helper.load_config()


class CommandTests(unittest.TestCase):
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
        with self.assertRaisesRegex(helper.RoadmapError, "provided together"):
            helper._progress_message(partial)
        invalid = mock.Mock(message="Working", state=None, phase=None, completed=4, total=3, next_step=None)
        with self.assertRaisesRegex(helper.RoadmapError, "0 <= completed"):
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
        with self.assertRaisesRegex(helper.RoadmapError, "actively claimed"):
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
        with self.assertRaisesRegex(helper.RoadmapError, "0 <= completed"):
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
        self.assertTrue(str(calls[-1][2]["idempotency_key"]).startswith("tc-roadmap-"))
        self.assertFalse(any(path.endswith("/claim") for _, path, _ in calls))

    def test_backlog_requires_a_backlog_column(self) -> None:
        class StubClient:
            def call(self, method: str, path: str, **kwargs):  # type: ignore[no-untyped-def]
                if path == "/projects?limit=200":
                    return {"data": [{"id": "project-1", "key": "TC"}]}, {}
                return {"data": [{"id": "column-1", "semantic_state": "active"}]}, {}

        args = mock.Mock(project="TC", operation_id="plan-2")
        with self.assertRaisesRegex(helper.RoadmapError, "no backlog column"):
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
        with self.assertRaisesRegex(helper.RoadmapError, "operation_id"):
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

            with mock.patch.dict(os.environ, {"CODEX_SESSION_ID": "session-heartbeat", "TC_ROADMAP_STATE_DIR": raw}, clear=False):
                result = helper.cmd_heartbeat(StubClient(), args)  # type: ignore[arg-type]
            self.assertEqual(calls[0][0:2], ("POST", "/tasks/TC-35/heartbeat"))
            self.assertEqual(calls[0][2]["body"], {"operation_id": "heartbeat-1"})
            self.assertNotIn("if_match", calls[0][2])
            self.assertNotIn("idempotency_key", calls[0][2])
            self.assertEqual(result["operation_id"], "heartbeat-1")
            self.assertIsNotNone(store.load().last_heartbeat_at)  # type: ignore[union-attr]


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
            with self.assertRaises(helper.RoadmapError) as caught:
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
            with self.assertRaises(helper.RoadmapError):
                client.call("GET", "/tasks/task-1")
        self.assertEqual(opener.open.call_count, 1)
        sleep.assert_not_called()


if __name__ == "__main__":
    unittest.main(verbosity=2)
