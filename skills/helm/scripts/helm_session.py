#!/usr/bin/env python3
"""Private, retry-safe local state for the Helm Codex hooks.

The state file is intentionally a very small coordination record. It is not a
transcript cache: narrative progress, phases, next actions, working
directories, raw session identifiers, and credentials are rejected at the
serialization boundary.
"""

from __future__ import annotations

import fcntl
import hashlib
import json
import os
import stat
import tempfile
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable, Iterator, Mapping


SCHEMA_VERSION = 1
STATE_DIR_ENV = "HELM_STATE_DIR"
ALT_STATE_DIR_ENV = "HELM_HOOK_STATE_DIR"
LEGACY_STATE_DIR_ENV = "TC_ROADMAP_STATE_DIR"
LEGACY_ALT_STATE_DIR_ENV = "TC_ROADMAP_HOOK_STATE_DIR"
MAX_STATE_BYTES = 64 * 1024
MAX_TEXT_LENGTH = 256
MAX_OPERATION_ID_LENGTH = 128
MAX_TIMESTAMP_LENGTH = 64
HEARTBEAT_INTERVAL_SECONDS = 8 * 60

_AGENT_STATES = frozenset(("working", "waiting", "verifying", "handoff"))
_SAFE_IDENTIFIER_CHARS = frozenset("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.:/")
_STATE_FIELDS = frozenset(
    {
        "schema",
        "task_id",
        "task_key",
        "project_id",
        "operation_id",
        "agent_state",
        "checkpoint_completed",
        "checkpoint_total",
        "last_progress_at",
        "last_heartbeat_at",
        "snapshot_ready",
    }
)


class SessionStateError(RuntimeError):
    """A local state error safe for callers to report without sensitive data."""


@dataclass
class SessionState:
    """The allowlisted state persisted for one Codex session."""

    schema: int = SCHEMA_VERSION
    task_id: str = ""
    task_key: str = ""
    project_id: str = ""
    operation_id: str = ""
    agent_state: str = "working"
    checkpoint_completed: int | None = None
    checkpoint_total: int | None = None
    last_progress_at: str | None = None
    last_heartbeat_at: str | None = None
    # A heartbeat is valid only after the server has a matching structured
    # snapshot. This remains safe metadata and prevents an early hook pulse
    # from being rejected by the heartbeat endpoint.
    snapshot_ready: bool = False

    def to_dict(self) -> dict[str, Any]:
        return _validate_state(
            {
                "schema": self.schema,
                "task_id": self.task_id,
                "task_key": self.task_key,
                "project_id": self.project_id,
                "operation_id": self.operation_id,
                "agent_state": self.agent_state,
                "checkpoint_completed": self.checkpoint_completed,
                "checkpoint_total": self.checkpoint_total,
                "last_progress_at": self.last_progress_at,
                "last_heartbeat_at": self.last_heartbeat_at,
                "snapshot_ready": self.snapshot_ready,
            }
        )

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> "SessionState":
        normalized = _validate_state(value)
        return cls(**normalized)


def _safe_text(value: Any, field_name: str, *, max_length: int = MAX_TEXT_LENGTH, required: bool = False) -> str:
    if not isinstance(value, str):
        raise SessionStateError(f"local state field {field_name} is invalid")
    value = value.strip()
    if required and not value:
        raise SessionStateError(f"local state field {field_name} is missing")
    if len(value) > max_length or any(ord(char) < 0x20 or ord(char) == 0x7F for char in value):
        raise SessionStateError(f"local state field {field_name} is invalid")
    return value


def _safe_identifier(value: Any, field_name: str, *, max_length: int = MAX_TEXT_LENGTH, required: bool = False) -> str:
    value = _safe_text(value, field_name, max_length=max_length, required=required)
    if value and any(char not in _SAFE_IDENTIFIER_CHARS for char in value):
        raise SessionStateError(f"local state field {field_name} is invalid")
    return value


def _timestamp(value: Any, field_name: str) -> str | None:
    if value is None or value == "":
        return None
    value = _safe_text(value, field_name, max_length=MAX_TIMESTAMP_LENGTH)
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise SessionStateError(f"local state field {field_name} is invalid") from exc
    if parsed.tzinfo is None:
        raise SessionStateError(f"local state field {field_name} is invalid")
    return value


def _validate_state(value: Mapping[str, Any]) -> dict[str, Any]:
    if not isinstance(value, Mapping):
        raise SessionStateError("local state must contain an object")
    unknown = set(value) - _STATE_FIELDS
    if unknown:
        # Do not echo field names: a future caller might accidentally supply a
        # sensitive key and even the key itself should not reach hook output.
        raise SessionStateError("local state contains unsupported metadata")
    schema = value.get("schema", SCHEMA_VERSION)
    if schema != SCHEMA_VERSION:
        raise SessionStateError("local state schema is unsupported")
    task_id = _safe_identifier(value.get("task_id", ""), "task_id", required=True)
    task_key = _safe_identifier(value.get("task_key", ""), "task_key", required=False)
    project_id = _safe_identifier(value.get("project_id", ""), "project_id", required=True)
    operation_id = _safe_identifier(value.get("operation_id", ""), "operation_id", max_length=MAX_OPERATION_ID_LENGTH, required=True)
    agent_state = _safe_identifier(value.get("agent_state", "working"), "agent_state", required=True)
    if agent_state not in _AGENT_STATES:
        raise SessionStateError("local state agent state is unsupported")

    completed = value.get("checkpoint_completed")
    total = value.get("checkpoint_total")
    if completed is not None and (isinstance(completed, bool) or not isinstance(completed, int)):
        raise SessionStateError("local state checkpoint count is invalid")
    if total is not None and (isinstance(total, bool) or not isinstance(total, int)):
        raise SessionStateError("local state checkpoint count is invalid")
    if (completed is None) != (total is None):
        raise SessionStateError("local state checkpoint counts must be paired")
    if total is not None and (total < 1 or total > 100 or completed < 0 or completed > total):
        raise SessionStateError("local state checkpoint count is invalid")

    snapshot_ready = value.get("snapshot_ready", False)
    if not isinstance(snapshot_ready, bool):
        raise SessionStateError("local state flag is invalid")
    if snapshot_ready and agent_state not in {"working", "waiting", "verifying", "handoff"}:
        raise SessionStateError("local state snapshot flag is invalid")

    return {
        "schema": SCHEMA_VERSION,
        "task_id": task_id,
        "task_key": task_key,
        "project_id": project_id,
        "operation_id": operation_id,
        "agent_state": agent_state,
        "checkpoint_completed": completed,
        "checkpoint_total": total,
        "last_progress_at": _timestamp(value.get("last_progress_at"), "last_progress_at"),
        "last_heartbeat_at": _timestamp(value.get("last_heartbeat_at"), "last_heartbeat_at"),
        "snapshot_ready": snapshot_ready,
    }


def utc_now() -> datetime:
    return datetime.now(timezone.utc)


def timestamp(value: datetime | None = None) -> str:
    return (value or utc_now()).astimezone(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def session_identifier(environ: Mapping[str, str] | None = None) -> str | None:
    """Get a raw session identifier without ever persisting it."""

    environ = os.environ if environ is None else environ
    value = str(environ.get("CODEX_SESSION_ID", "")).strip()
    if value:
        return value
    value = str(environ.get("CODEX_THREAD_ID", "")).strip()
    return value or None


def hook_identifier(event: Mapping[str, Any]) -> str | None:
    """Extract a hook session/thread identifier from event JSON.

    Codex hook payloads have used both snake_case and camelCase names. A small
    amount of nested lookup keeps the parser tolerant without storing any of
    the identifiers.
    """

    if not isinstance(event, Mapping):
        return None
    containers: list[Mapping[str, Any]] = [event]
    for key in ("session", "context", "payload"):
        nested = event.get(key)
        if isinstance(nested, Mapping):
            containers.append(nested)
    for container in containers:
        for key in ("session_id", "sessionId", "thread_id", "threadId"):
            value = container.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
    return None


def session_key(identifier: str) -> str:
    """Hash a session/thread ID for use as a filename."""

    if not isinstance(identifier, str) or not identifier.strip():
        raise SessionStateError("session identifier is missing")
    return hashlib.sha256(identifier.strip().encode("utf-8")).hexdigest()


def default_state_dir(environ: Mapping[str, str] | None = None) -> Path:
    environ = os.environ if environ is None else environ
    configured = (
        str(environ.get(STATE_DIR_ENV, "")).strip()
        or str(environ.get(ALT_STATE_DIR_ENV, "")).strip()
        or str(environ.get(LEGACY_STATE_DIR_ENV, "")).strip()
        or str(environ.get(LEGACY_ALT_STATE_DIR_ENV, "")).strip()
    )
    if configured:
        return Path(configured).expanduser()
    codex_home = str(environ.get("CODEX_HOME", "")).strip()
    base = Path(codex_home).expanduser() if codex_home else Path.home() / ".codex"
    # The state root is a private compatibility contract. Existing sessions
    # must remain visible to the canonical hook after the skill is renamed.
    return base / "hook-state" / "tc-roadmap"


def _check_directory(path: Path, *, create: bool) -> None:
    """Create/check a private state directory and reject unsafe paths."""

    # ``absolute`` normalizes a relative test override without resolving
    # symlinks. Every component is then inspected with lstat below.
    path = path.expanduser().absolute()
    # Validate every existing path component before creating descendants. A
    # single ``mkdir(parents=True)`` would otherwise follow a symlinked parent
    # and place private state in an unexpected location.
    missing: list[Path] = []
    current = path
    while True:
        try:
            info = current.lstat()
        except FileNotFoundError:
            missing.append(current)
            if current.parent == current:
                break
            current = current.parent
            continue
        except OSError as exc:
            raise SessionStateError("local state directory is unavailable") from exc
        if stat.S_ISLNK(info.st_mode):
            raise SessionStateError("local state directory is a symlink")
        if not stat.S_ISDIR(info.st_mode):
            raise SessionStateError("local state path is not a directory")
        if current.parent == current:
            break
        current = current.parent
    if missing and create:
        for child in reversed(missing):
            try:
                child.mkdir(mode=0o700)
            except FileExistsError:
                pass
            except OSError as exc:
                raise SessionStateError("local state directory is unavailable") from exc
    if missing and not create:
        return

    # Recheck the complete chain after creation. Besides validating ordinary
    # pre-existing parents, this closes the race where a missing intermediate
    # component is replaced with a symlink between the first inspection and
    # mkdir.
    current = path
    while True:
        try:
            component = current.lstat()
        except OSError as exc:
            raise SessionStateError("local state directory is unavailable") from exc
        if stat.S_ISLNK(component.st_mode):
            raise SessionStateError("local state directory is a symlink")
        if not stat.S_ISDIR(component.st_mode):
            raise SessionStateError("local state path is not a directory")
        if current.parent == current:
            break
        current = current.parent
    try:
        info = path.stat()
    except OSError as exc:
        raise SessionStateError("local state directory is unavailable") from exc
    if not stat.S_ISDIR(info.st_mode):
        raise SessionStateError("local state path is not a directory")
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) & 0o077:
        raise SessionStateError("local state directory permissions are unsafe")
    # chmod can be affected by a caller's umask. Keep the required mode
    # explicit for newly created directories, while refusing broader modes.
    if stat.S_IMODE(info.st_mode) != 0o700:
        try:
            path.chmod(0o700)
        except OSError as exc:
            raise SessionStateError("local state directory permissions are unsafe") from exc


def _check_file(path: Path, expected_mode: int = 0o600) -> None:
    try:
        info = path.lstat()
    except FileNotFoundError:
        return
    except OSError as exc:
        raise SessionStateError("local state file is unavailable") from exc
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise SessionStateError("local state file is unsafe")
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != expected_mode:
        raise SessionStateError("local state file permissions are unsafe")


@contextmanager
def _locked(lock_path: Path) -> Iterator[int]:
    _check_file(lock_path)
    flags = os.O_RDWR | os.O_CREAT
    nofollow = getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open(lock_path, flags | nofollow, 0o600)
    except OSError as exc:
        raise SessionStateError("local state lock is unavailable") from exc
    try:
        os.fchmod(fd, 0o600)
        info = os.fstat(fd)
        if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise SessionStateError("local state lock permissions are unsafe")
        fcntl.flock(fd, fcntl.LOCK_EX)
        yield fd
    except OSError as exc:
        raise SessionStateError("local state lock is unavailable") from exc
    finally:
        try:
            fcntl.flock(fd, fcntl.LOCK_UN)
        finally:
            os.close(fd)


def _read_path(path: Path) -> SessionState | None:
    _check_file(path)
    if not path.exists():
        return None
    try:
        with path.open("rb") as stream:
            raw = stream.read(MAX_STATE_BYTES + 1)
    except OSError as exc:
        raise SessionStateError("local state file is unavailable") from exc
    if len(raw) > MAX_STATE_BYTES:
        raise SessionStateError("local state file is too large")
    try:
        value = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise SessionStateError("local state file is invalid") from exc
    return SessionState.from_dict(value)


def _atomic_write(path: Path, state: SessionState) -> None:
    encoded = json.dumps(state.to_dict(), ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    if len(encoded) > MAX_STATE_BYTES:
        raise SessionStateError("local state is too large")
    _check_file(path)
    temp_name: str | None = None
    fd = -1
    try:
        fd, temp_name = tempfile.mkstemp(prefix=".helm-", suffix=".tmp", dir=path.parent)
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "wb") as stream:
            fd = -1
            stream.write(encoded)
            stream.flush()
            os.fsync(stream.fileno())
        # Recheck before replace so an unsafe pre-existing target is never
        # silently accepted. os.replace itself is atomic within this directory.
        _check_file(path)
        os.replace(temp_name, path)
        temp_name = None
        try:
            dir_fd = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
            try:
                os.fsync(dir_fd)
            finally:
                os.close(dir_fd)
        except OSError:
            # The data replacement already succeeded; directory fsync is a
            # durability best effort on filesystems that do not support it.
            pass
    except OSError as exc:
        raise SessionStateError("local state write failed") from exc
    finally:
        if fd >= 0:
            os.close(fd)
        if temp_name:
            try:
                os.unlink(temp_name)
            except OSError:
                pass


@dataclass
class HeartbeatResult:
    attempted: bool
    state: SessionState | None = None
    response: Any = None


@dataclass
class StateStore:
    """A keyed state file with atomic updates and per-session locking."""

    identifier: str
    directory: Path | str | None = None
    now_fn: Callable[[], datetime] = field(default=utc_now, repr=False)

    def __post_init__(self) -> None:
        if not isinstance(self.identifier, str) or not self.identifier.strip():
            raise SessionStateError("session identifier is missing")
        self.identifier = self.identifier.strip()
        self.directory = Path(self.directory).expanduser() if self.directory is not None else default_state_dir()
        # Keep a stable path across CLI and hook invocations even when a test
        # or development override is relative. Do not resolve symlinks here;
        # _check_directory must be able to reject them component by component.
        self.directory = Path(self.directory).absolute()
        self.key = session_key(self.identifier)

    @classmethod
    def from_env(cls, *, directory: Path | str | None = None, environ: Mapping[str, str] | None = None, now_fn: Callable[[], datetime] = utc_now) -> "StateStore | None":
        identifier = session_identifier(environ)
        if not identifier:
            return None
        if directory is None:
            directory = default_state_dir(environ)
        return cls(identifier, directory=directory, now_fn=now_fn)

    @classmethod
    def from_event(cls, event: Mapping[str, Any], *, directory: Path | str | None = None, now_fn: Callable[[], datetime] = utc_now) -> "StateStore | None":
        identifier = hook_identifier(event)
        return cls(identifier, directory=directory, now_fn=now_fn) if identifier else None

    @property
    def path(self) -> Path:
        return self.directory / f"{self.key}.json"

    @property
    def lock_path(self) -> Path:
        return self.directory / f"{self.key}.lock"

    def _prepare(self) -> None:
        _check_directory(Path(self.directory), create=True)

    @contextmanager
    def _lock(self) -> Iterator[None]:
        self._prepare()
        with _locked(self.lock_path):
            yield

    def load(self) -> SessionState | None:
        with self._lock():
            return _read_path(self.path)

    def save(self, state: SessionState | Mapping[str, Any]) -> SessionState:
        normalized = state if isinstance(state, SessionState) else SessionState.from_dict(state)
        # Round-trip through validation even for a SessionState supplied by a
        # caller that manually changed an attribute.
        normalized = SessionState.from_dict(normalized.to_dict())
        with self._lock():
            _atomic_write(self.path, normalized)
        return normalized

    def update(self, **changes: Any) -> SessionState:
        with self._lock():
            current = _read_path(self.path)
            if current is None:
                raise SessionStateError("local state is not initialized")
            value = current.to_dict()
            value.update(changes)
            updated = SessionState.from_dict(value)
            _atomic_write(self.path, updated)
            return updated

    def clear_matching(self, *, task_id: str, operation_id: str | None = None) -> bool:
        task_id = _safe_identifier(task_id, "task_id", required=True)
        operation_id = _safe_identifier(operation_id, "operation_id", max_length=MAX_OPERATION_ID_LENGTH, required=False) if operation_id is not None else None
        with self._lock():
            current = _read_path(self.path)
            if current is None or current.task_id != task_id or (operation_id is not None and current.operation_id != operation_id):
                return False
            _check_file(self.path)
            try:
                self.path.unlink()
            except FileNotFoundError:
                return False
            except OSError as exc:
                raise SessionStateError("local state cleanup failed") from exc
            return True

    def heartbeat_if_due(
        self,
        send: Callable[[str, str], Any],
        *,
        interval_seconds: float = HEARTBEAT_INTERVAL_SECONDS,
        now: datetime | None = None,
    ) -> HeartbeatResult:
        """Send one heartbeat while holding the lock, then atomically record it.

        Keeping the lock through the network call means concurrent PostToolUse
        and PreCompact hooks cannot both observe the same due timestamp and
        send duplicate heartbeats. A failed send leaves the old timestamp in
        place so a later hook can retry.
        """

        if interval_seconds < 0:
            raise SessionStateError("heartbeat interval is invalid")
        observed = (now or self.now_fn()).astimezone(timezone.utc)
        with self._lock():
            current = _read_path(self.path)
            if current is None or not current.snapshot_ready:
                return HeartbeatResult(False, current)
            timestamps = [value for value in (current.last_progress_at, current.last_heartbeat_at) if value]
            if timestamps:
                try:
                    previous = max(
                        datetime.fromisoformat(value.replace("Z", "+00:00")).astimezone(timezone.utc)
                        for value in timestamps
                    )
                except ValueError as exc:
                    raise SessionStateError("local state heartbeat timestamp is invalid") from exc
                if (observed - previous).total_seconds() < interval_seconds:
                    return HeartbeatResult(False, current)
            response = send(current.task_id, current.operation_id)
            updated = SessionState.from_dict({**current.to_dict(), "last_heartbeat_at": timestamp(observed)})
            _atomic_write(self.path, updated)
            return HeartbeatResult(True, updated, response)

    def stop_decision(self) -> tuple[bool, SessionState | None]:
        """Return whether active work should block the current Stop event.

        The hook protocol's ``stop_hook_active`` flag guards the immediate
        continuation after a block. No persistent marker is needed (or
        desirable): a later, independent Stop event should be checked again if
        the agent still reports working/verifying.
        """

        with self._lock():
            current = _read_path(self.path)
            return bool(current and current.agent_state in {"working", "verifying"}), current


def warning(exc: BaseException) -> str:
    """Return a sanitized warning without path, identifier, or credentials."""

    return f"helm: local session state unavailable ({type(exc).__name__}); continuing"


__all__ = [
    "HEARTBEAT_INTERVAL_SECONDS",
    "SCHEMA_VERSION",
    "SessionState",
    "SessionStateError",
    "StateStore",
    "HeartbeatResult",
    "default_state_dir",
    "hook_identifier",
    "session_identifier",
    "session_key",
    "timestamp",
    "utc_now",
    "warning",
]
