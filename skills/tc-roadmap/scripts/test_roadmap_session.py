#!/usr/bin/env python3
"""Focused tests for the private Roadmap session state store."""

from __future__ import annotations

import json
import os
import tempfile
import threading
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest import mock

import roadmap_session as session


def _state(**changes: object) -> session.SessionState:
    values: dict[str, object] = {
        "task_id": "task-1",
        "task_key": "TC-1",
        "project_id": "project-1",
        "operation_id": "session/progress-1",
        "agent_state": "working",
    }
    values.update(changes)
    return session.SessionState(**values)  # type: ignore[arg-type]


class SessionIdentityTests(unittest.TestCase):
    def test_session_key_is_stable_and_fallback_prefers_session(self) -> None:
        self.assertEqual(session.session_key("session-1"), session.session_key(" session-1 "))
        self.assertNotEqual(session.session_key("session-1"), session.session_key("thread-1"))
        self.assertEqual(session.session_identifier({"CODEX_SESSION_ID": "s", "CODEX_THREAD_ID": "t"}), "s")
        self.assertEqual(session.session_identifier({"CODEX_THREAD_ID": "t"}), "t")
        self.assertIsNone(session.session_identifier({}))

    def test_hook_identifier_accepts_nested_and_camel_case_ids(self) -> None:
        self.assertEqual(session.hook_identifier({"session_id": "s"}), "s")
        self.assertEqual(session.hook_identifier({"context": {"threadId": "t"}}), "t")
        self.assertIsNone(session.hook_identifier({"session_id": ""}))


class StateStoreTests(unittest.TestCase):
    def test_private_modes_atomic_json_and_allowlisted_metadata(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw) / "state"
            store = session.StateStore("session-1", directory=root)
            store.save(_state(checkpoint_completed=1, checkpoint_total=2, snapshot_ready=True))
            self.assertEqual(root.stat().st_mode & 0o777, 0o700)
            self.assertEqual(store.path.stat().st_mode & 0o777, 0o600)
            payload = json.loads(store.path.read_text(encoding="utf-8"))
            self.assertEqual(payload["task_key"], "TC-1")
            self.assertNotIn("summary", payload)
            self.assertNotIn("phase", payload)
            self.assertNotIn("next_action", payload)
            self.assertNotIn("cwd", payload)
            self.assertNotIn("session_id", payload)
            self.assertNotIn("thread_id", payload)
            self.assertEqual(store.load(), _state(checkpoint_completed=1, checkpoint_total=2, snapshot_ready=True))

    def test_unknown_sensitive_metadata_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            store = session.StateStore("session-1", directory=raw)
            with self.assertRaises(session.SessionStateError):
                store.save({**_state().to_dict(), "summary": "secret"})
            self.assertFalse(store.path.exists())

    def test_unsafe_directory_and_symlink_are_refused(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw) / "state"
            root.mkdir(mode=0o755)
            store = session.StateStore("session-1", directory=root)
            with self.assertRaises(session.SessionStateError):
                store.save(_state())

            safe = Path(raw) / "safe"
            safe.mkdir(mode=0o700)
            target = Path(raw) / "target"
            target.mkdir(mode=0o700)
            os.symlink(target, safe / "link")
            linked = session.StateStore("session-2", directory=safe / "link")
            with self.assertRaises(session.SessionStateError):
                linked.save(_state())

            nested = session.StateStore("session-3", directory=safe / "link" / "nested")
            with self.assertRaises(session.SessionStateError):
                nested.save(_state())

    def test_concurrent_updates_never_leave_partial_json(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            store = session.StateStore("session-1", directory=raw)
            store.save(_state())
            errors: list[BaseException] = []

            def writer(number: int) -> None:
                try:
                    for _ in range(30):
                        store.update(checkpoint_completed=number, checkpoint_total=3)
                        loaded = store.load()
                        self.assertIsNotNone(loaded)
                except BaseException as exc:  # pragma: no cover - diagnostic
                    errors.append(exc)

            threads = [threading.Thread(target=writer, args=(number,)) for number in (0, 1, 2)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()
            self.assertEqual(errors, [])
            self.assertIsInstance(json.loads(store.path.read_text(encoding="utf-8")), dict)

    def test_heartbeat_uses_newest_progress_or_heartbeat_timestamp(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            start = datetime(2026, 1, 1, tzinfo=timezone.utc)
            store = session.StateStore("session-1", directory=raw, now_fn=lambda: start)
            store.save(_state(snapshot_ready=True, last_progress_at=session.timestamp(start)))
            sent: list[tuple[str, str]] = []

            result = store.heartbeat_if_due(lambda task, operation: sent.append((task, operation)), now=start + timedelta(minutes=7))
            self.assertFalse(result.attempted)
            self.assertEqual(sent, [])
            result = store.heartbeat_if_due(lambda task, operation: sent.append((task, operation)), now=start + timedelta(minutes=8))
            self.assertTrue(result.attempted)
            self.assertEqual(sent, [("task-1", "session/progress-1")])
            self.assertEqual(store.load().last_heartbeat_at, "2026-01-01T00:08:00Z")  # type: ignore[union-attr]

    def test_heartbeat_failure_does_not_advance_throttle(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            start = datetime(2026, 1, 1, tzinfo=timezone.utc)
            store = session.StateStore("session-1", directory=raw, now_fn=lambda: start)
            store.save(_state(snapshot_ready=True))
            with self.assertRaisesRegex(RuntimeError, "network"):
                store.heartbeat_if_due(lambda _task, _operation: (_ for _ in ()).throw(RuntimeError("network")), now=start + timedelta(minutes=9))
            self.assertIsNone(store.load().last_heartbeat_at)  # type: ignore[union-attr]


if __name__ == "__main__":
    unittest.main(verbosity=2)
