#!/usr/bin/env python3
"""Unit tests for the TC Roadmap helper."""

from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import tc_roadmap as helper


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

        args = mock.Mock(project="TC")
        with self.assertRaisesRegex(helper.RoadmapError, "no backlog column"):
            helper.cmd_backlog(StubClient(), args)  # type: ignore[arg-type]


if __name__ == "__main__":
    unittest.main(verbosity=2)
