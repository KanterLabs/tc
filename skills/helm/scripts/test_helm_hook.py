#!/usr/bin/env python3
"""Focused tests for the Helm lifecycle hook adapter."""

from __future__ import annotations

import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest import mock

import helm_session as session
import helm_hook as hook


def _state(**changes: object) -> session.SessionState:
    values: dict[str, object] = {
        "task_id": "task-1",
        "task_key": "TC-1",
        "project_id": "project-1",
        "operation_id": "session/op-1",
        "agent_state": "working",
        "snapshot_ready": True,
    }
    values.update(changes)
    return session.SessionState(**values)  # type: ignore[arg-type]


class HookTests(unittest.TestCase):
    def make_store(self, root: str, state: session.SessionState | None = None) -> session.StateStore:
        store = session.StateStore("session-1", directory=root)
        if state is not None:
            store.save(state)
        return store

    def test_parser_accepts_event_aliases_and_rejects_non_objects(self) -> None:
        self.assertEqual(hook.parse_event('{"event":"SessionStart","session_id":"s"}')['event'], "SessionStart")
        self.assertEqual(hook._event_name({"event_name": "post_tool_use"}), "PostToolUse")
        with self.assertRaises(ValueError):
            hook.parse_event("[]")

    def test_compact_session_start_injects_safe_recovery_context(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            store = self.make_store(raw, _state(checkpoint_completed=2, checkpoint_total=4))
            event = {"hook_event_name": "SessionStart", "source": "compact", "session_id": "session-1"}
            result = hook.handle_event(event, store_factory=lambda _event: store)
            context = result["hookSpecificOutput"]["additionalContext"]
            self.assertIn("Recovered Helm task TC-1", context)
            self.assertIn("agent state working", context)
            self.assertIn("checkpoints 2/4", context)
            self.assertIn("operation session/op-1", context)
            self.assertNotIn("session-1", context)
            self.assertNotIn("summary", context)

    def test_initial_snapshot_reminder_and_no_heartbeat_before_progress(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            store = self.make_store(raw, _state(snapshot_ready=False))
            heartbeat = mock.Mock()
            event = {"hook_event_name": "PostToolUse", "session_id": "session-1"}
            result = hook.handle_event(event, store_factory=lambda _event: store, heartbeat_fn=heartbeat)
            self.assertIn("hookSpecificOutput", result)
            self.assertIn("additionalContext", result["hookSpecificOutput"])
            self.assertIn("initial structured progress", result["hookSpecificOutput"]["additionalContext"])
            heartbeat.assert_not_called()

    def test_heartbeat_is_throttled_and_uses_only_safe_call(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            start = datetime(2026, 1, 1, tzinfo=timezone.utc)
            store = self.make_store(raw, _state(last_progress_at="2025-12-31T23:50:00Z"))
            sent: list[session.StateStore] = []
            heartbeat = lambda value: sent.append(value)  # noqa: E731
            with mock.patch.dict("os.environ", {"HELM_HEARTBEAT_INTERVAL_SECONDS": "480"}, clear=False):
                result = hook.handle_event({"event": "PostToolUse", "session_id": "session-1"}, store_factory=lambda _event: store, now=start, heartbeat_fn=heartbeat)
            self.assertEqual(result, {})
            self.assertEqual(sent, [store])

            # A heartbeat callback that does not touch state is still a valid
            # injected test double; the store itself enforces the throttle.
            store.update(last_heartbeat_at="2026-01-01T00:01:00Z")
            sent.clear()
            with mock.patch.dict("os.environ", {"HELM_HEARTBEAT_INTERVAL_SECONDS": "480"}, clear=False):
                hook.handle_event({"event": "PostToolUse", "session_id": "session-1"}, store_factory=lambda _event: store, now=start + timedelta(minutes=2), heartbeat_fn=heartbeat)
            self.assertEqual(sent, [])

    def test_network_failure_fails_open(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            store = self.make_store(raw, _state(last_progress_at="2025-12-31T23:00:00Z"))
            with mock.patch.object(hook, "_heartbeat", side_effect=RuntimeError("network secret")):
                with mock.patch("sys.stderr") as stderr:
                    result = hook.handle_event({"event": "PreCompact", "session_id": "session-1"}, store_factory=lambda _event: store, now=datetime(2026, 1, 1, tzinfo=timezone.utc))
            self.assertEqual(result, {})
            self.assertNotIn("secret", "".join(str(call) for call in stderr.write.call_args_list))

    def test_stop_blocks_once_then_stop_hook_guard_allows_continuation(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            store = self.make_store(raw, _state(agent_state="verifying"))
            event = {"event": "Stop", "session_id": "session-1"}
            first = hook.handle_event(event, store_factory=lambda _event: store)
            self.assertEqual(first["decision"], "block")
            self.assertIn("TC-1", first["reason"])
            continuation = hook.handle_event({**event, "stop_hook_active": True}, store_factory=lambda _event: store)
            self.assertEqual(continuation, {})

    def test_waiting_handoff_missing_id_and_unknown_event_allow_stop(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            store = self.make_store(raw, _state(agent_state="waiting"))
            self.assertEqual(hook.handle_event({"event": "Stop", "session_id": "session-1"}, store_factory=lambda _event: store), {})
            self.assertEqual(hook.handle_event({"event": "Stop"}), {})
            self.assertEqual(hook.handle_event({"event": "Unknown", "session_id": "session-1"}, store_factory=lambda _event: store), {})


if __name__ == "__main__":
    unittest.main(verbosity=2)
