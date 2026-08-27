#!/usr/bin/env python3
"""Track agent work in TC Roadmap without exposing credentials."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import stat
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib import error, parse, request


DEFAULT_BASE_URL = "https://tc.shanekanterman.dev"
DEFAULT_CONFIG = Path("~/.config/tc-roadmap/credentials.json").expanduser()
MAX_RESPONSE_BYTES = 4 * 1024 * 1024


class RoadmapError(RuntimeError):
    """A safe-to-display Roadmap client error."""


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

    def call(
        self,
        method: str,
        path: str,
        *,
        body: dict[str, Any] | None = None,
        if_match: int | None = None,
        idempotency_key: str | None = None,
    ) -> tuple[Any, dict[str, str]]:
        headers = {
            "Accept": "application/json",
            "Authorization": f"Bearer {self.config.token}",
            "User-Agent": "tc-roadmap-skill/1",
        }
        if self.config.cf_access_client_id:
            headers["CF-Access-Client-Id"] = self.config.cf_access_client_id
            headers["CF-Access-Client-Secret"] = self.config.cf_access_client_secret
        data = None
        if body is not None:
            data = json.dumps(body, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if if_match is not None:
            headers["If-Match"] = f'"v{if_match}"'
        if idempotency_key:
            headers["Idempotency-Key"] = idempotency_key
        target = self.config.base_url + "/api/v1" + path
        req = request.Request(target, method=method, headers=headers, data=data)
        try:
            with request.build_opener(_NoRedirect).open(req, timeout=15) as response:
                raw = response.read(MAX_RESPONSE_BYTES + 1)
                response_headers = {key.lower(): value for key, value in response.headers.items()}
        except error.HTTPError as exc:
            raw = exc.read(MAX_RESPONSE_BYTES + 1)
            message = _error_message(raw)
            raise RoadmapError(f"TC Roadmap returned HTTP {exc.code}: {message}") from exc
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
    try:
        payload = json.loads(raw[:MAX_RESPONSE_BYTES])
    except json.JSONDecodeError:
        return "request failed"
    if isinstance(payload, dict):
        error_value = payload.get("error")
        if isinstance(error_value, dict) and isinstance(error_value.get("message"), str):
            return error_value["message"][:300]
    return "request failed"


def _data(payload: Any) -> list[dict[str, Any]]:
    if not isinstance(payload, dict) or not isinstance(payload.get("data"), list):
        raise RoadmapError("TC Roadmap returned an unexpected collection")
    return [item for item in payload["data"] if isinstance(item, dict)]


def _idempotency(operation_id: str, action: str, detail: str = "") -> str:
    seed = "\0".join((operation_id.strip(), action, detail)).encode("utf-8")
    return "tc-roadmap-" + hashlib.sha256(seed).hexdigest()


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
    project = _project(client, args.project)
    project_id = str(project["id"])
    columns, _ = client.call("GET", f"/projects/{parse.quote(project_id, safe='')}/columns?limit=200")
    active = next((item for item in _data(columns) if item.get("semantic_state") == "active"), None)
    if not active:
        raise RoadmapError(f"project has no active column: {args.project}")
    description = "Goal: " + args.goal.strip()
    if args.step:
        description += "\n\nCheckpoints\n" + "\n".join(f"- [ ] {step.strip()}" for step in args.step)
    created, _ = client.call(
        "POST",
        f"/projects/{parse.quote(project_id, safe='')}/tasks",
        body={"title": args.title.strip(), "description": description, "column_id": active["id"], "priority": args.priority},
        idempotency_key=_idempotency(args.operation_id, "create"),
    )
    if not isinstance(created, dict) or not isinstance(created.get("version"), int):
        raise RoadmapError("TC Roadmap returned an unexpected created task")
    claimed, _ = client.call(
        "POST",
        "/tasks/" + parse.quote(str(created["id"]), safe="") + "/claim",
        body={"lease_seconds": args.lease_seconds},
        if_match=created["version"],
        idempotency_key=_idempotency(args.operation_id, "claim"),
    )
    return {"task": claimed, "operation_id": args.operation_id}


def cmd_resume(client: Client, args: argparse.Namespace) -> Any:
    current = _task(client, args.task)
    action = "renew" if current.get("claimed_by") else "claim"
    claimed, _ = client.call(
        "POST",
        "/tasks/" + parse.quote(str(current["id"]), safe="") + "/" + action,
        body={"lease_seconds": args.lease_seconds},
        if_match=current["version"],
        idempotency_key=_idempotency(args.operation_id, action + "-existing"),
    )
    return {"task": claimed, "operation_id": args.operation_id}


def cmd_progress(client: Client, args: argparse.Namespace) -> Any:
    current = _task(client, args.task)
    comment, _ = client.call(
        "POST",
        "/tasks/" + parse.quote(str(current["id"]), safe="") + "/comments",
        body={"body": args.message.strip()},
        idempotency_key=_idempotency(args.operation_id, "progress", args.message.strip()),
    )
    return {"task": current.get("key", current["id"]), "comment": comment}


def _action(client: Client, args: argparse.Namespace, action: str, field: str) -> Any:
    current = _task(client, args.task)
    value = getattr(args, field)
    payload, _ = client.call(
        "POST",
        "/tasks/" + parse.quote(str(current["id"]), safe="") + "/" + action,
        body={field: value.strip()},
        if_match=current["version"],
        idempotency_key=_idempotency(args.operation_id, action, value.strip()),
    )
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

    resume = subparsers.add_parser("resume", help="Claim or renew an existing task")
    resume.add_argument("--task", required=True)
    resume.add_argument("--lease-seconds", type=int, choices=range(30, 604801), default=604800)
    resume.add_argument("--operation-id", required=True)
    resume.set_defaults(handler=cmd_resume)

    progress = subparsers.add_parser("progress", help="Post a meaningful progress comment")
    progress.add_argument("--task", required=True)
    progress.add_argument("--message", required=True)
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
