#!/usr/bin/env python3
"""Codex lifecycle hooks for the TC Roadmap agent-work pulse.

Hooks are deliberately conservative: they read/write only the private,
allowlisted session state and may refresh agent-work liveness. They never
invent progress, comments, completion, or block actions on behalf of an agent.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import datetime
from typing import Any, Callable, Mapping
from urllib.parse import quote

import roadmap_session
import tc_roadmap


_EVENT_ALIASES = {
    "session_start": "SessionStart",
    "sessionstart": "SessionStart",
    "post_tool_use": "PostToolUse",
    "posttooluse": "PostToolUse",
    "pre_compact": "PreCompact",
    "precompact": "PreCompact",
    "stop": "Stop",
}


def _event_name(event: Mapping[str, Any], explicit: str | None = None) -> str:
    raw = explicit
    if raw is None:
        for key in ("hook_event_name", "hookEventName", "event_name", "event", "type", "name"):
            candidate = event.get(key)
            if isinstance(candidate, str) and candidate.strip():
                raw = candidate
                break
    normalized = str(raw or "").strip().replace("-", "_").replace(" ", "_").casefold()
    return _EVENT_ALIASES.get(normalized, str(raw or "").strip())


def parse_event(raw: str | bytes) -> dict[str, Any]:
    """Parse one hook JSON object without retaining the raw payload."""

    try:
        value = json.loads(raw)
    except (TypeError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ValueError("hook event is not valid JSON") from exc
    if not isinstance(value, dict):
        raise ValueError("hook event must be a JSON object")
    return value


def _is_compact(event: Mapping[str, Any]) -> bool:
    for key in ("source", "reason", "trigger", "compact_reason"):
        value = event.get(key)
        if isinstance(value, str) and value.strip().casefold() in {"compact", "compaction", "precompact"}:
            return True
    return False


def _context(state: roadmap_session.SessionState, *, compact: bool = False) -> str:
    task = state.task_key or state.task_id
    project = f" in project {state.project_id}" if state.project_id else ""
    checkpoint = ""
    if state.checkpoint_completed is not None and state.checkpoint_total is not None:
        checkpoint = f"; checkpoints {state.checkpoint_completed}/{state.checkpoint_total}"
    prefix = "Recovered" if compact else "Active"
    reminder = "; publish the initial structured progress snapshot before heartbeat" if not state.snapshot_ready else ""
    return f"{prefix} Roadmap task {task}{project}: agent state {state.agent_state}{checkpoint}; operation {state.operation_id}{reminder}."


def _safe_empty() -> dict[str, Any]:
    return {}


def _truthy(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        return value.strip().casefold() in {"1", "true", "yes", "on"}
    return False


def _interval_seconds(environ: Mapping[str, str] | None = None) -> float:
    environ = os.environ if environ is None else environ
    for key in ("TC_ROADMAP_HEARTBEAT_INTERVAL_SECONDS", "TC_ROADMAP_HEARTBEAT_INTERVAL"):
        raw = str(environ.get(key, "")).strip()
        if not raw:
            continue
        try:
            value = float(raw)
        except ValueError:
            break
        if value >= 0:
            return value
        break
    return roadmap_session.HEARTBEAT_INTERVAL_SECONDS


def _warning(exc: BaseException) -> None:
    print(roadmap_session.warning(exc), file=sys.stderr)


def _heartbeat(store: roadmap_session.StateStore, *, now: datetime | None = None) -> roadmap_session.HeartbeatResult:
    """Run a due liveness heartbeat with the client built only after state is ready."""

    def send(task_id: str, operation_id: str) -> Any:
        # Build the authenticated client only after the locked state store has
        # confirmed that a heartbeat is due. Not-due hooks therefore do not
        # touch credentials or emit avoidable configuration warnings.
        config = tc_roadmap.load_config()
        client = tc_roadmap.Client(config)
        # Heartbeat is intentionally no-If-Match and no-idempotency. The
        # endpoint only touches an agent-work liveness timestamp and is safe to
        # replay.
        return client.call(
            "POST",
            "/tasks/" + quote(task_id, safe="") + "/heartbeat",
            body={"operation_id": operation_id},
        )

    return store.heartbeat_if_due(
        send,
        interval_seconds=_interval_seconds(),
        now=now,
    )


def handle_event(
    event: Mapping[str, Any],
    *,
    event_name: str | None = None,
    now: datetime | None = None,
    store_factory: Callable[[Mapping[str, Any]], roadmap_session.StateStore | None] | None = None,
    heartbeat_fn: Callable[[roadmap_session.StateStore], roadmap_session.HeartbeatResult] | None = None,
) -> dict[str, Any]:
    """Handle a parsed hook event and return protocol JSON.

    Every error is intentionally fail-open. The hook must never prevent the
    user from continuing because credentials, the local state directory, or
    the Roadmap service is unavailable.
    """

    if not isinstance(event, Mapping):
        return _safe_empty()
    name = _event_name(event, event_name)
    if name not in {"SessionStart", "PostToolUse", "PreCompact", "Stop"}:
        return _safe_empty()
    if name == "Stop" and _truthy(event.get("stop_hook_active")):
        return _safe_empty()
    try:
        store = (store_factory or roadmap_session.StateStore.from_event)(event)
    except Exception as exc:
        _warning(exc)
        return _safe_empty()
    if store is None:
        return _safe_empty()

    try:
        if name == "SessionStart":
            state = store.load()
            if state is None:
                return _safe_empty()
            context = _context(state, compact=_is_compact(event))
            return {
                "hookSpecificOutput": {
                    "hookEventName": "SessionStart",
                    "additionalContext": context,
                }
            }

        if name in {"PostToolUse", "PreCompact"}:
            state = store.load()
            if state is None:
                return _safe_empty()
            # A waiting/handoff state is a deliberate pause/handoff and should
            # not be kept alive by automatic hook activity. Before the initial
            # structured snapshot, expose only a concise reminder at events
            # that can accept context; never send a rejected heartbeat.
            if not state.snapshot_ready:
                if name == "PostToolUse":
                    return {
                        "hookSpecificOutput": {
                            "hookEventName": "PostToolUse",
                            "additionalContext": _context(state),
                        }
                    }
                return _safe_empty()
            if state.agent_state not in {"working", "verifying"}:
                return _safe_empty()
            if heartbeat_fn is not None:
                # Test/integration doubles represent the network send. Keep
                # the real locked due-check around them so injected clients
                # observe the same concurrency/throttle behavior.
                store.heartbeat_if_due(
                    lambda _task, _operation: heartbeat_fn(store),
                    interval_seconds=_interval_seconds(),
                    now=now,
                )
            else:
                _heartbeat(store, now=now)
            return _safe_empty()

        should_block, state = store.stop_decision()
        if should_block and state is not None:
            task = state.task_key or state.task_id
            return {
                "decision": "block",
                "reason": f"Roadmap task {task} is still marked {state.agent_state}; complete it, block it, or publish structured waiting/handoff progress before stopping.",
            }
        return _safe_empty()
    except (roadmap_session.SessionStateError, tc_roadmap.RoadmapError, OSError) as exc:
        _warning(exc)
        return _safe_empty()
    except Exception as exc:  # pragma: no cover - final fail-open boundary
        # Hook integrations should never expose a traceback, credential, or
        # event payload if an unexpected library failure occurs.
        _warning(exc)
        return _safe_empty()


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--event", help="Override the event name in the JSON payload")
    return parser


def main(argv: list[str] | None = None) -> int:
    try:
        args = build_parser().parse_args(argv)
        value = parse_event(sys.stdin.buffer.read())
        result = handle_event(value, event_name=args.event)
    except Exception as exc:
        _warning(exc)
        result = _safe_empty()
    json.dump(result, sys.stdout, ensure_ascii=False, separators=(",", ":"))
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
