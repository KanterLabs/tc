#!/usr/bin/env python3
"""Track agent work in TC Roadmap without exposing credentials."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import stat
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib import error, parse, request

import roadmap_session


DEFAULT_BASE_URL = "https://tc.shanekanterman.dev"
DEFAULT_CONFIG = Path("~/.config/tc-roadmap/credentials.json").expanduser()
MAX_RESPONSE_BYTES = 4 * 1024 * 1024
MAX_TRANSIENT_RETRIES = 2
MAX_RETRY_DELAY_SECONDS = 5.0


class RoadmapError(RuntimeError):
    """A safe-to-display Roadmap client error.

    HTTP failures retain a small amount of structured, non-sensitive context so
    callers can make safe retry decisions without parsing human-facing text.
    """

    def __init__(
        self,
        message: str,
        *,
        status_code: int | None = None,
        error_code: str = "",
        retry_after: str | None = None,
    ) -> None:
        super().__init__(message)
        self.status_code = status_code
        # ``status``/``http_code`` preserve the HTTP status while
        # ``error_code``/``api_code`` preserve the server's machine code.
        self.status = status_code
        self.http_code = status_code
        self.error_code = error_code
        self.api_code = error_code
        self.code = status_code
        self.retry_after = retry_after


@dataclass(frozen=True)
class Config:
    base_url: str
    token: str
    cf_access_client_id: str = ""
    cf_access_client_secret: str = ""


def _credential_path() -> Path:
    configured = os.environ.get("TC_ROADMAP_CONFIG", "").strip()
    return Path(configured).expanduser() if configured else DEFAULT_CONFIG


def _read_config_file(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    info = path.stat()
    if info.st_uid != os.getuid():
        raise RoadmapError(f"credential file is not owned by the current user: {path}")
    if stat.S_IMODE(info.st_mode) & 0o077:
        raise RoadmapError(f"credential file permissions are too broad: {path}; use mode 0600")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise RoadmapError(f"cannot read credential file {path}: {type(exc).__name__}") from exc
    if not isinstance(value, dict):
        raise RoadmapError(f"credential file must contain a JSON object: {path}")
    return value


def load_config() -> Config:
    values = _read_config_file(_credential_path())
    base_url = os.environ.get("TC_ROADMAP_URL", str(values.get("base_url", DEFAULT_BASE_URL))).strip().rstrip("/")
    token = os.environ.get(
        "TC_ROADMAP_TOKEN",
        os.environ.get("ROADMAP_TOKEN", str(values.get("token", ""))),
    ).strip()
    client_id = os.environ.get("TC_CF_ACCESS_CLIENT_ID", str(values.get("cf_access_client_id", ""))).strip()
    client_secret = os.environ.get("TC_CF_ACCESS_CLIENT_SECRET", str(values.get("cf_access_client_secret", ""))).strip()
    parsed = parse.urlsplit(base_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.username or parsed.password:
        raise RoadmapError("TC Roadmap URL must be an HTTP(S) origin without credentials")
    if parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
        raise RoadmapError("TC Roadmap URL must not contain a path, query, or fragment")
    if parsed.scheme != "https" and parsed.hostname not in {"127.0.0.1", "localhost", "::1"}:
        raise RoadmapError("TC Roadmap URL must use HTTPS except on loopback")
    if not token:
        raise RoadmapError("TC Roadmap agent token is not configured")
    if bool(client_id) != bool(client_secret):
        raise RoadmapError("both Cloudflare Access credential fields must be configured together")
    return Config(base_url, token, client_id, client_secret)


class _NoRedirect(request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # type: ignore[no-untyped-def]
        return None


class Client:
    def __init__(self, config: Config) -> None:
        self.config = config

    @staticmethod
    def _retry_safe(method: str, path: str, idempotency_key: str | None) -> bool:
        """Return whether a request can be replayed after a transient error.

        GET is naturally replayable. Normal writes are replayable only when
        they carry the helper's deterministic idempotency key. Heartbeat is a
        deliberately idempotent server-side timestamp touch and therefore is
        safe even though it intentionally has no idempotency key.
        """

        if method.upper() in {"GET", "HEAD", "OPTIONS"}:
            return True
        return bool(idempotency_key) or (
            method.upper() == "POST" and path.rstrip("/").endswith("/heartbeat")
        )

    @staticmethod
    def _retry_delay(headers: dict[str, str], attempt: int) -> float:
        """Choose a bounded delay while preserving the server's hint.

        ``Retry-After`` can be either a number of seconds or an HTTP date. A
        malformed/far-future value falls back to a small exponential delay;
        no caller is ever made to sleep for more than five seconds.
        """

        raw = headers.get("retry-after", "").strip()
        delay: float | None = None
        if raw:
            try:
                delay = max(0.0, float(raw))
            except ValueError:
                try:
                    retry_at = datetime.strptime(raw, "%a, %d %b %Y %H:%M:%S GMT").replace(tzinfo=timezone.utc)
                    delay = max(0.0, (retry_at - datetime.now(timezone.utc)).total_seconds())
                except ValueError:
                    delay = None
        if delay is None:
            delay = min(0.5 * (2**attempt), MAX_RETRY_DELAY_SECONDS)
        return min(delay, MAX_RETRY_DELAY_SECONDS)

    def call(
        self,
        method: str,
        path: str,
        *,
        body: dict[str, Any] | None = None,
        if_match: int | None = None,
        idempotency_key: str | None = None,
    ) -> tuple[Any, dict[str, str]]:
        data = None
        if body is not None:
            data = json.dumps(body, separators=(",", ":")).encode("utf-8")
        target = self.config.base_url + "/api/v1" + path
        retry_safe = self._retry_safe(method, path, idempotency_key)
        for attempt in range(MAX_TRANSIENT_RETRIES + 1):
            headers = {
                "Accept": "application/json",
                "Authorization": f"Bearer {self.config.token}",
                "User-Agent": "tc-roadmap-skill/1",
            }
            if self.config.cf_access_client_id:
                headers["CF-Access-Client-Id"] = self.config.cf_access_client_id
                headers["CF-Access-Client-Secret"] = self.config.cf_access_client_secret
            if body is not None:
                headers["Content-Type"] = "application/json"
            if if_match is not None:
                headers["If-Match"] = f'"v{if_match}"'
            if idempotency_key:
                headers["Idempotency-Key"] = idempotency_key
            req = request.Request(target, method=method, headers=headers, data=data)
            try:
                with request.build_opener(_NoRedirect).open(req, timeout=15) as response:
                    raw = response.read(MAX_RESPONSE_BYTES + 1)
                    response_headers = {key.lower(): value for key, value in response.headers.items()}
            except error.HTTPError as exc:
                response_headers = (
                    {key.lower(): value for key, value in exc.headers.items()}
                    if exc.headers is not None
                    else {}
                )
                try:
                    raw = exc.read(MAX_RESPONSE_BYTES + 1)
                finally:
                    exc.close()
                if exc.code in {429, 503} and retry_safe and attempt < MAX_TRANSIENT_RETRIES:
                    time.sleep(self._retry_delay(response_headers, attempt))
                    continue
                message, error_code = _error_details(raw)
                raise RoadmapError(
                    f"TC Roadmap returned HTTP {exc.code}: {message}",
                    status_code=exc.code,
                    error_code=error_code,
                    retry_after=response_headers.get("retry-after"),
                ) from exc
            except (error.URLError, TimeoutError, OSError) as exc:
                raise RoadmapError(f"cannot reach TC Roadmap: {type(exc).__name__}") from exc
            if len(raw) > MAX_RESPONSE_BYTES:
                raise RoadmapError("TC Roadmap response exceeded the safe size limit")
            if not raw:
                return None, response_headers
            try:
                return json.loads(raw), response_headers
            except json.JSONDecodeError as exc:
                raise RoadmapError("TC Roadmap returned a non-JSON response") from exc


def _error_message(raw: bytes) -> str:
    return _error_details(raw)[0]


def _error_details(raw: bytes) -> tuple[str, str]:
    try:
        payload = json.loads(raw[:MAX_RESPONSE_BYTES])
    except json.JSONDecodeError:
        return "request failed", ""
    if isinstance(payload, dict):
        error_value = payload.get("error")
        if isinstance(error_value, dict):
            message = error_value.get("message")
            code = error_value.get("code")
            if isinstance(message, str):
                return message[:300], code[:120] if isinstance(code, str) else ""
    return "request failed", ""


def _data(payload: Any) -> list[dict[str, Any]]:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), list):
        raise RoadmapError("TC Roadmap returned an unexpected collection")
    return [item for item in payload["data"] if isinstance(item, dict)]


def _idempotency(operation_id: str, action: str, detail: str = "") -> str:
    seed = "\0".join((operation_id.strip(), action, detail)).encode("utf-8")
    return "tc-roadmap-" + hashlib.sha256(seed).hexdigest()


def _mutation_idempotency(operation_id: str, method: str, path: str, detail: Any = "") -> str:
    """Build a deterministic key scoped to one mutation target.

    Operation IDs are intentionally caller-owned and may be reused for a
    whole session. Including the HTTP method/path and canonical body detail
    prevents a retry for one task or endpoint from replaying a different
    mutation that happens to use the same operation ID.
    """

    if not isinstance(detail, str):
        detail = _canonical_json(detail)
    target = method.upper().strip() + " " + path.strip()
    return _idempotency(operation_id, target, detail)


def _validate_operation_id(operation_id: Any) -> str:
    """Apply the server's operation ID contract before any network write."""

    if not isinstance(operation_id, str):
        raise RoadmapError("operation_id must be between 1 and 128 safe identifier characters")
    value = operation_id.strip()
    if not value or len(value) > 128:
        raise RoadmapError("operation_id must be between 1 and 128 safe identifier characters")
    ascii_alnum = lambda char: ("a" <= char <= "z") or ("A" <= char <= "Z") or ("0" <= char <= "9")
    if not (ascii_alnum(value[0]) and all(ascii_alnum(char) or char in "-_.:/" for char in value)):
        raise RoadmapError("operation_id must be between 1 and 128 safe identifier characters")
    return value


def _task(client: Client, reference: str) -> dict[str, Any]:
    payload, _ = client.call("GET", "/tasks/" + parse.quote(reference, safe=""))
    if not isinstance(payload, dict) or not isinstance(payload.get("version"), int):
        raise RoadmapError("TC Roadmap returned an unexpected task")
    return payload


def _project(client: Client, reference: str) -> dict[str, Any]:
    payload, _ = client.call("GET", "/projects?limit=200")
    folded = reference.casefold()
    for project in _data(payload):
        candidates = (project.get("id"), project.get("key"), project.get("slug"), project.get("name"))
        if any(isinstance(item, str) and item.casefold() == folded for item in candidates):
            return project
    raise RoadmapError(f"project not found: {reference}")


def _session_store() -> roadmap_session.StateStore | None:
    """Return the current session store, or ``None`` when no ID is exposed."""

    try:
        return roadmap_session.StateStore.from_env()
    except roadmap_session.SessionStateError as exc:
        _state_warning(exc)
        return None


def _state_warning(exc: BaseException) -> None:
    # Storage is advisory for the network CLI. Never turn an otherwise
    # successful Roadmap mutation into a failure, and never print a path or
    # caller-provided metadata in the warning.
    print(roadmap_session.warning(exc), file=sys.stderr)


def _task_identity(task: Any, project_id: str = "") -> tuple[str, str, str] | None:
    if not isinstance(task, dict):
        return None
    task_id = task.get("id")
    if not isinstance(task_id, str) or not task_id.strip():
        return None
    task_key = task.get("key", "")
    if not isinstance(task_key, str):
        task_key = ""
    resolved_project = task.get("project_id")
    if not isinstance(resolved_project, str) or not resolved_project.strip():
        resolved_project = project_id
    if not isinstance(resolved_project, str) or not resolved_project.strip():
        return None
    return task_id.strip(), task_key.strip(), resolved_project.strip()


def _record_session_start(task: Any, project_id: str, operation_id: str, *, snapshot_ready: bool = False) -> None:
    store = _session_store()
    if store is None:
        return
    identity = _task_identity(task, project_id)
    if identity is None:
        _state_warning(roadmap_session.SessionStateError("task identity is unavailable"))
        return
    task_id, task_key, resolved_project = identity
    try:
        store.save(
            roadmap_session.SessionState(
                task_id=task_id,
                task_key=task_key,
                project_id=resolved_project,
                operation_id=operation_id,
                agent_state="working",
                last_progress_at=roadmap_session.timestamp(),
                snapshot_ready=snapshot_ready,
            )
        )
    except roadmap_session.SessionStateError as exc:
        _state_warning(exc)


def _record_session_progress(task: Any, previous: Any, payload: dict[str, Any]) -> None:
    store = _session_store()
    if store is None:
        return
    project_id = ""
    if isinstance(task, dict) and isinstance(task.get("project_id"), str):
        project_id = task["project_id"]
    if not project_id and isinstance(previous, dict) and isinstance(previous.get("project_id"), str):
        project_id = previous["project_id"]
    identity = _task_identity(task, project_id) or _task_identity(previous, project_id)
    if identity is None:
        return
    task_id, task_key, resolved_project = identity
    try:
        store.save(
            roadmap_session.SessionState(
                task_id=task_id,
                task_key=task_key,
                project_id=resolved_project,
                operation_id=str(payload.get("operation_id", "")),
                agent_state=str(payload.get("state", "working")),
                checkpoint_completed=payload.get("checkpoint_completed"),
                checkpoint_total=payload.get("checkpoint_total"),
                last_progress_at=roadmap_session.timestamp(),
                snapshot_ready=True,
            )
        )
    except roadmap_session.SessionStateError as exc:
        _state_warning(exc)


def _record_session_heartbeat(task: Any, args: argparse.Namespace) -> None:
    store = _session_store()
    if store is None:
        return
    try:
        current = store.load()
        if current is None or current.operation_id != args.operation_id.strip():
            return
        response_task = task
        if isinstance(task, dict) and isinstance(task.get("task"), dict):
            response_task = task["task"]
        response_id = response_task.get("id") if isinstance(response_task, dict) else None
        reference = str(args.task).strip()
        if isinstance(response_id, str) and response_id.strip():
            if current.task_id != response_id.strip():
                return
        elif reference not in {current.task_id, current.task_key}:
            return
        store.update(last_heartbeat_at=roadmap_session.timestamp())
    except roadmap_session.SessionStateError as exc:
        _state_warning(exc)


def _clear_matching_session(task: Any, operation_id: str) -> None:
    store = _session_store()
    if store is None:
        return
    if not isinstance(task, dict) or not isinstance(task.get("id"), str):
        return
    try:
        store.clear_matching(task_id=task["id"], operation_id=operation_id)
    except roadmap_session.SessionStateError as exc:
        _state_warning(exc)


def cmd_projects(client: Client, _args: argparse.Namespace) -> Any:
    payload, _ = client.call("GET", "/projects?limit=200")
    return {"projects": [{key: item.get(key) for key in ("id", "key", "slug", "name")} for item in _data(payload)]}


def cmd_tasks(client: Client, args: argparse.Namespace) -> Any:
    project = _project(client, args.project)
    query = "?limit=200"
    if args.query:
        query += "&q=" + parse.quote(args.query)
    payload, _ = client.call("GET", f"/projects/{parse.quote(str(project['id']), safe='')}/tasks{query}")
    return {"project": project.get("key"), "tasks": _data(payload)}


def cmd_start(client: Client, args: argparse.Namespace) -> Any:
    args.operation_id = _validate_operation_id(args.operation_id)
    project = _project(client, args.project)
    project_id = str(project["id"])
    columns, _ = client.call("GET", f"/projects/{parse.quote(project_id, safe='')}/columns?limit=200")
    active = next((item for item in _data(columns) if item.get("semantic_state") == "active"), None)
    if not active:
        raise RoadmapError(f"project has no active column: {args.project}")
    description = "Goal: " + args.goal.strip()
    if args.step:
        description += "\n\nCheckpoints\n" + "\n".join(f"- [ ] {step.strip()}" for step in args.step)
    create_path = f"/projects/{parse.quote(project_id, safe='')}/tasks"
    created, _ = client.call(
        "POST",
        create_path,
        body={"title": args.title.strip(), "description": description, "column_id": active["id"], "priority": args.priority},
        idempotency_key=_mutation_idempotency(args.operation_id, "POST", create_path, {
            "title": args.title.strip(),
            "description": description,
            "column_id": active["id"],
            "priority": args.priority,
        }),
    )
    if not isinstance(created, dict) or not isinstance(created.get("version"), int):
        raise RoadmapError("TC Roadmap returned an unexpected created task")
    claim_path = "/tasks/" + parse.quote(str(created["id"]), safe="") + "/claim"
    claim_body = {"lease_seconds": args.lease_seconds}
    claimed, _ = client.call(
        "POST",
        claim_path,
        body=claim_body,
        if_match=created["version"],
        idempotency_key=_mutation_idempotency(args.operation_id, "POST", claim_path, claim_body),
    )
    if not isinstance(claimed, dict) or not isinstance(claimed.get("version"), int):
        raise RoadmapError("TC Roadmap returned an unexpected claimed task")
    _record_session_start(claimed, project_id, args.operation_id)
    return {"task": claimed, "operation_id": args.operation_id}


def cmd_backlog(client: Client, args: argparse.Namespace) -> Any:
    args.operation_id = _validate_operation_id(args.operation_id)
    project = _project(client, args.project)
    project_id = str(project["id"])
    columns, _ = client.call("GET", f"/projects/{parse.quote(project_id, safe='')}/columns?limit=200")
    backlog = next((item for item in _data(columns) if item.get("semantic_state") == "backlog"), None)
    if not backlog:
        raise RoadmapError(f"project has no backlog column: {args.project}")
    description = "Goal: " + args.goal.strip()
    if args.step:
        description += "\n\nAcceptance criteria\n" + "\n".join(f"- [ ] {step.strip()}" for step in args.step)
    create_path = f"/projects/{parse.quote(project_id, safe='')}/tasks"
    create_body = {"title": args.title.strip(), "description": description, "column_id": backlog["id"], "priority": args.priority}
    created, _ = client.call(
        "POST",
        create_path,
        body=create_body,
        idempotency_key=_mutation_idempotency(args.operation_id, "POST", create_path, create_body),
    )
    if not isinstance(created, dict) or not isinstance(created.get("version"), int):
        raise RoadmapError("TC Roadmap returned an unexpected created task")
    return {"task": created, "operation_id": args.operation_id}


def cmd_resume(client: Client, args: argparse.Namespace) -> Any:
    args.operation_id = _validate_operation_id(args.operation_id)
    current = _task(client, args.task)
    # The claim endpoint is intentionally used for both an unclaimed task and
    # a same-owner reclaim. The server treats a same-owner POST /claim as a
    # lease renewal, which makes a retry safe after a lost response and avoids
    # making a race-prone claim/renew decision from a stale GET.
    action = "claim"
    task_id = str(current["id"])
    claim_path = "/tasks/" + parse.quote(task_id, safe="") + "/claim"
    claim_body = {"lease_seconds": args.lease_seconds}
    claimed, _ = client.call(
        "POST",
        claim_path,
        body=claim_body,
        if_match=current["version"],
        idempotency_key=_mutation_idempotency(args.operation_id, "POST", claim_path, claim_body),
    )
    if not isinstance(claimed, dict) or not isinstance(claimed.get("version"), int):
        raise RoadmapError("TC Roadmap returned an unexpected claimed task")

    # Claiming does not alter board placement. Resolve the project's semantic
    # active column and move the task with an optimistic PATCH only when the
    # current column is not already active. A claim conflict from another actor
    # raises before this block, so a foreign active claim is never moved.
    project_id = str(current.get("project_id") or claimed.get("project_id") or "")
    if not project_id:
        raise RoadmapError("TC Roadmap returned an unexpected task project")
    current_column = claimed.get("column_id", current.get("column_id"))
    current_semantic = claimed.get("semantic_state", current.get("semantic_state"))
    if current_semantic is None:
        for candidate in (claimed, current):
            nested_column = candidate.get("column") if isinstance(candidate, dict) else None
            if isinstance(nested_column, dict) and nested_column.get("semantic_state") is not None:
                current_semantic = nested_column.get("semantic_state")
                break
    if current_semantic == "active":
        moved = claimed
    else:
        columns_path = f"/projects/{parse.quote(project_id, safe='')}/columns?limit=200"
        columns, _ = client.call("GET", columns_path)
        active = next((item for item in _data(columns) if item.get("semantic_state") == "active"), None)
        if not active or not isinstance(active.get("id"), str) or not active["id"].strip():
            raise RoadmapError("project has no active column")

        active_id = active["id"]
        if current_column == active_id:
            moved = claimed
        else:
            patch_path = "/tasks/" + parse.quote(task_id, safe="")
            patch_body = {"column_id": active_id}
            moved, _ = client.call(
                "PATCH",
                patch_path,
                body=patch_body,
                if_match=claimed["version"],
                idempotency_key=_mutation_idempotency(args.operation_id, "PATCH", patch_path, patch_body),
            )
            if not isinstance(moved, dict) or not isinstance(moved.get("version"), int):
                raise RoadmapError("TC Roadmap returned an unexpected moved task")
    snapshot_ready = False
    for candidate in (moved, claimed):
        if not isinstance(candidate, dict) or not isinstance(candidate.get("agent_work"), dict):
            continue
        work = candidate["agent_work"]
        if work.get("operation_id") != args.operation_id:
            continue
        claimed_by = candidate.get("claimed_by")
        work_actor = work.get("actor_id")
        if isinstance(claimed_by, str) and isinstance(work_actor, str) and claimed_by.strip() != work_actor.strip():
            continue
        snapshot_ready = True
        break
    _record_session_start(moved, project_id, args.operation_id, snapshot_ready=snapshot_ready)
    return {"task": moved, "operation_id": args.operation_id}


def cmd_progress(client: Client, args: argparse.Namespace) -> Any:
    args.operation_id = _validate_operation_id(args.operation_id)
    current = _task(client, args.task)
    if _structured_progress_supplied(args):
        if not _active_claim(current):
            raise RoadmapError("structured progress requires an actively claimed task")
        payload = _structured_progress_payload(args)
        path = "/tasks/" + parse.quote(str(current["id"]), safe="") + "/progress"
        updated, _ = client.call(
            "POST",
            path,
            body=payload,
            if_match=current["version"],
            idempotency_key=_mutation_idempotency(args.operation_id, "POST", path, payload),
        )
        if not isinstance(updated, dict) or not isinstance(updated.get("version"), int):
            raise RoadmapError("TC Roadmap returned an unexpected updated task")
        _record_session_progress(updated, current, payload)
        return {"task": updated, "operation_id": args.operation_id}
    message = _progress_message(args)
    path = "/tasks/" + parse.quote(str(current["id"]), safe="") + "/comments"
    comment_body = {"body": message}
    comment, _ = client.call(
        "POST",
        path,
        body=comment_body,
        idempotency_key=_mutation_idempotency(args.operation_id, "POST", path, comment_body),
    )
    return {"task": current.get("key", current["id"]), "comment": comment}


def cmd_heartbeat(client: Client, args: argparse.Namespace) -> Any:
    """Refresh agent-work liveness without changing the task version.

    Heartbeats deliberately omit both If-Match and Idempotency-Key. The server
    operation is an inherently idempotent timestamp touch, and persisting an
    idempotency row for every hook pulse would create unnecessary write load.
    """

    args.operation_id = _validate_operation_id(args.operation_id)
    task_id = parse.quote(str(args.task), safe="")
    path = "/tasks/" + task_id + "/heartbeat"
    body = {"operation_id": args.operation_id}
    payload, _ = client.call("POST", path, body=body)
    current = payload if isinstance(payload, dict) else {"id": args.task}
    _record_session_heartbeat(current, args)
    return {"task": payload, "operation_id": args.operation_id}


def _structured_progress_supplied(args: argparse.Namespace) -> bool:
    """Return whether the caller selected the structured progress API."""

    for field in ("state", "phase", "completed", "total", "next_step"):
        if getattr(args, field, None) is not None:
            return True
    refs = getattr(args, "checkpoint_refs", ())
    return isinstance(refs, (list, tuple)) and bool(refs)


def _active_claim(task: dict[str, Any]) -> bool:
    """Check the claim data available in a task response before posting."""

    claimed_by = task.get("claimed_by")
    if not isinstance(claimed_by, str) or not claimed_by.strip():
        return False
    # Older task representations may expose the claimant without the lease
    # timestamp. Let the progress endpoint remain authoritative in that case,
    # while rejecting an explicitly absent, malformed, or expired lease.
    if "claim_expires_at" not in task:
        return True
    expires_at = task.get("claim_expires_at")
    if not isinstance(expires_at, str) or not expires_at.strip():
        return False
    try:
        expires = datetime.fromisoformat(expires_at.replace("Z", "+00:00"))
    except ValueError:
        return False
    if expires.tzinfo is None:
        expires = expires.replace(tzinfo=timezone.utc)
    return expires > datetime.now(timezone.utc)


def _structured_progress_payload(args: argparse.Namespace) -> dict[str, Any]:
    completed = getattr(args, "completed", None)
    total = getattr(args, "total", None)
    if (completed is None) != (total is None):
        raise RoadmapError("completed and total must be provided together")
    if completed is not None and (total < 1 or total > 100 or completed < 0 or completed > total):
        raise RoadmapError("progress must satisfy 0 <= completed <= total <= 100")

    operation_id = _validate_operation_id(getattr(args, "operation_id", ""))
    state = getattr(args, "state", None)
    message = getattr(args, "message", "")
    payload: dict[str, Any] = {
        "operation_id": operation_id.strip(),
        "state": state or "working",
        "summary": message.strip(),
    }
    phase = getattr(args, "phase", None)
    if phase is not None:
        phase = phase.strip()
        if not phase:
            raise RoadmapError("phase must not be empty")
        payload["phase"] = phase
    next_step = getattr(args, "next_step", None)
    if next_step is not None:
        next_step = next_step.strip()
        if not next_step:
            raise RoadmapError("next action must not be empty")
        payload["next_action"] = next_step
    if completed is not None:
        payload["checkpoint_completed"] = completed
        payload["checkpoint_total"] = total

    refs = getattr(args, "checkpoint_refs", ())
    if not isinstance(refs, (list, tuple)):
        refs = ()
    if refs:
        normalized_refs = [ref.strip() if isinstance(ref, str) else "" for ref in refs]
        if any(not isinstance(ref, str) or not ref for ref in normalized_refs):
            raise RoadmapError("checkpoint references must be non-empty")
        if len(normalized_refs) > 100:
            raise RoadmapError("checkpoint references must contain at most 100 items")
        if len(set(normalized_refs)) != len(normalized_refs):
            raise RoadmapError("checkpoint references must not contain duplicates")
        if any(len(ref) > 128 for ref in normalized_refs):
            raise RoadmapError("checkpoint references must be at most 128 characters")
        if completed is None:
            raise RoadmapError("checkpoint counts are required when checkpoint refs are provided")
        if len(normalized_refs) != total:
            raise RoadmapError("checkpoint refs must match checkpoint total")
        payload["checkpoint_refs"] = normalized_refs
    return payload


def _canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def _progress_message(args: argparse.Namespace) -> str:
    completed = args.completed
    total = args.total
    if (completed is None) != (total is None):
        raise RoadmapError("completed and total must be provided together")
    if completed is not None and (total < 1 or completed < 0 or completed > total):
        raise RoadmapError("progress must satisfy 0 <= completed <= total and total >= 1")
    if not any((args.state, args.phase, args.next_step, completed is not None)):
        return args.message.strip()
    heading = ["Agent update"]
    if args.state:
        heading.append(args.state.capitalize())
    if args.phase:
        heading.append(args.phase.strip())
    if completed is not None:
        heading.append(f"{completed}/{total}")
    lines = [" · ".join(heading), "", args.message.strip()]
    if args.next_step:
        lines.extend(("", "Next: " + args.next_step.strip()))
    return "\n".join(lines)


def _action(client: Client, args: argparse.Namespace, action: str, field: str) -> Any:
    args.operation_id = _validate_operation_id(args.operation_id)
    current = _task(client, args.task)
    value = getattr(args, field)
    path = "/tasks/" + parse.quote(str(current["id"]), safe="") + "/" + action
    body = {field: value.strip()}
    payload, _ = client.call(
        "POST",
        path,
        body=body,
        if_match=current["version"],
        idempotency_key=_mutation_idempotency(args.operation_id, "POST", path, body),
    )
    _clear_matching_session(current, args.operation_id)
    return {"task": payload, "operation_id": args.operation_id}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    projects = subparsers.add_parser("projects", help="List visible projects")
    projects.set_defaults(handler=cmd_projects)

    tasks = subparsers.add_parser("tasks", help="List or search tasks in a project")
    tasks.add_argument("--project", required=True)
    tasks.add_argument("--query", default="")
    tasks.set_defaults(handler=cmd_tasks)

    start = subparsers.add_parser("start", help="Create and claim an active task")
    start.add_argument("--project", required=True)
    start.add_argument("--title", required=True)
    start.add_argument("--goal", required=True)
    start.add_argument("--step", action="append", default=[])
    start.add_argument("--priority", choices=("low", "normal", "high", "urgent"), default="normal")
    start.add_argument("--lease-seconds", type=int, choices=range(30, 604801), default=604800)
    start.add_argument("--operation-id", required=True)
    start.set_defaults(handler=cmd_start)

    backlog = subparsers.add_parser("backlog", help="Create an unclaimed backlog task")
    backlog.add_argument("--project", required=True)
    backlog.add_argument("--title", required=True)
    backlog.add_argument("--goal", required=True)
    backlog.add_argument("--step", action="append", default=[])
    backlog.add_argument("--priority", choices=("low", "normal", "high", "urgent"), default="normal")
    backlog.add_argument("--operation-id", required=True)
    backlog.set_defaults(handler=cmd_backlog)

    resume = subparsers.add_parser("resume", help="Claim or renew an existing task")
    resume.add_argument("--task", required=True)
    resume.add_argument("--lease-seconds", type=int, choices=range(30, 604801), default=604800)
    resume.add_argument("--operation-id", required=True)
    resume.set_defaults(handler=cmd_resume)

    heartbeat = subparsers.add_parser("heartbeat", help="Refresh agent-work liveness")
    heartbeat.add_argument("--task", required=True)
    heartbeat.add_argument("--operation-id", required=True)
    heartbeat.set_defaults(handler=cmd_heartbeat)

    progress = subparsers.add_parser("progress", help="Post a meaningful progress comment")
    progress.add_argument("--task", required=True)
    progress.add_argument("--message", required=True)
    progress.add_argument("--state", choices=("working", "waiting", "verifying", "handoff"))
    progress.add_argument("--phase")
    progress.add_argument("--completed", type=int)
    progress.add_argument("--total", type=int)
    progress.add_argument("--next", dest="next_step")
    progress.add_argument("--checkpoint-ref", action="append", dest="checkpoint_refs", default=[])
    progress.add_argument("--operation-id", required=True)
    progress.set_defaults(handler=cmd_progress)

    complete = subparsers.add_parser("complete", help="Complete a claimed task")
    complete.add_argument("--task", required=True)
    complete.add_argument("--comment", required=True)
    complete.add_argument("--operation-id", required=True)
    complete.set_defaults(handler=lambda client, args: _action(client, args, "complete", "comment"))

    block = subparsers.add_parser("block", help="Block a claimed task")
    block.add_argument("--task", required=True)
    block.add_argument("--reason", required=True)
    block.add_argument("--operation-id", required=True)
    block.set_defaults(handler=lambda client, args: _action(client, args, "block", "reason"))
    return parser


def main() -> int:
    try:
        args = build_parser().parse_args()
        result = args.handler(Client(load_config()), args)
        json.dump(result, sys.stdout, ensure_ascii=False, separators=(",", ":"))
        sys.stdout.write("\n")
        return 0
    except RoadmapError as exc:
        print(f"tc-roadmap: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
