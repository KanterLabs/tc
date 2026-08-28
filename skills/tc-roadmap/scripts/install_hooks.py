#!/usr/bin/env python3
"""Install the TC Roadmap lifecycle hooks without changing other hooks."""

from __future__ import annotations

import argparse
import json
import os
import shlex
import stat
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Any


SESSION_START_MATCHER = "startup|resume|clear|compact"
HOOK_EVENTS = ("SessionStart", "PostToolUse", "PreCompact", "Stop")
HOOK_RELATIVE_PATH = Path("skills") / "tc-roadmap" / "scripts" / "tc_roadmap_hook.py"
HOOK_MODE = 0o600
HOOK_TIMEOUTS = {
    "SessionStart": (5, "Refreshing TC Roadmap session"),
    "PostToolUse": (30, "Refreshing TC Roadmap liveness"),
    "PreCompact": (30, "Refreshing TC Roadmap liveness"),
    "Stop": (5, "Refreshing TC Roadmap session"),
}


class HookInstallError(RuntimeError):
    """A safe-to-display lifecycle hook installation error."""


@dataclass(frozen=True)
class HookInstallResult:
    """The observable result of one additive hooks reconciliation."""

    hooks_path: Path
    changed: bool
    added_events: tuple[str, ...] = ()
    mode_changed: bool = False

    @property
    def review_required(self) -> bool:
        """Whether Codex /hooks review and trust is required."""

        return self.changed


def codex_home() -> Path:
    configured = os.environ.get("CODEX_HOME", "").strip()
    return Path(configured).expanduser() if configured else Path.home() / ".codex"


def _current_uid() -> int:
    # Codex runs on Unix, but keeping this helper isolated makes the file easy
    # to exercise on platforms that do not expose os.getuid().
    getuid = getattr(os, "getuid", None)
    return int(getuid()) if getuid is not None else -1


def _validate_existing(path: Path, *, label: str) -> os.stat_result | None:
    """Validate one path without following a symlink."""

    try:
        info = os.lstat(path)
    except FileNotFoundError:
        return None
    except OSError as exc:
        raise HookInstallError(f"cannot inspect {label}") from exc
    if stat.S_ISLNK(info.st_mode):
        raise HookInstallError(f"refusing to use symlink {label}")
    uid = _current_uid()
    if uid >= 0 and info.st_uid != uid:
        raise HookInstallError(f"{label} is not owned by the current user")
    return info


def _validate_path_chain(path: Path) -> None:
    """Reject symlinked path components before creating or replacing files."""

    current = path
    while True:
        try:
            info = os.lstat(current)
        except FileNotFoundError:
            info = None
        except OSError as exc:
            raise HookInstallError("cannot inspect the hooks path") from exc
        if info is not None and stat.S_ISLNK(info.st_mode):
            raise HookInstallError("refusing to use a symlink in the hooks path")
        if current.parent == current:
            return
        current = current.parent


def _ensure_parent(path: Path) -> os.stat_result:
    _validate_path_chain(path)
    parent = path.parent
    info = _validate_existing(parent, label="hooks directory")
    if info is None:
        try:
            parent.mkdir(parents=True, mode=0o700, exist_ok=True)
        except OSError as exc:
            raise HookInstallError("cannot create the Codex hooks directory") from exc
        info = _validate_existing(parent, label="hooks directory")
    if info is None or not stat.S_ISDIR(info.st_mode):
        raise HookInstallError("Codex hooks parent is not a directory")
    return info


def _read_config(path: Path) -> tuple[dict[str, Any], os.stat_result | None]:
    info = _validate_existing(path, label="hooks.json")
    if info is None:
        return {}, None
    if not stat.S_ISREG(info.st_mode):
        raise HookInstallError("hooks.json is not a regular file")
    try:
        raw = path.read_text(encoding="utf-8")
        value = json.loads(raw, parse_constant=_reject_json_constant)
    except (OSError, UnicodeError, ValueError, json.JSONDecodeError) as exc:
        raise HookInstallError("hooks.json is not valid JSON") from exc
    if not isinstance(value, dict):
        raise HookInstallError("hooks.json must contain a JSON object")
    return value, info


def _reject_json_constant(name: str) -> None:
    raise ValueError(name)


def _validate_relevant_shape(config: dict[str, Any]) -> dict[str, Any]:
    if "hooks" not in config:
        hooks = {}
        config["hooks"] = hooks
    else:
        hooks = config["hooks"]
    if not isinstance(hooks, dict):
        raise HookInstallError("hooks.json 'hooks' field must be an object")

    # Validate only event groups we may touch. Unrelated policy-owned events
    # are intentionally left opaque and untouched.
    for event in HOOK_EVENTS:
        if event not in hooks:
            continue
        groups = hooks[event]
        if not isinstance(groups, list):
            raise HookInstallError(f"hooks.json event {event!r} must be an array")
        for group in groups:
            if not isinstance(group, dict):
                raise HookInstallError(f"hooks.json event {event!r} contains an invalid group")
            if "hooks" not in group or not isinstance(group["hooks"], list):
                raise HookInstallError(f"hooks.json event {event!r} contains an invalid hook group")
            if "matcher" in group and not isinstance(group["matcher"], str):
                raise HookInstallError(f"hooks.json event {event!r} contains an invalid matcher")
            if any(not isinstance(hook, dict) for hook in group["hooks"]):
                raise HookInstallError(f"hooks.json event {event!r} contains an invalid hook")
    return hooks


def _hook_command(home: Path) -> str:
    script = (home / HOOK_RELATIVE_PATH).expanduser()
    # A normal CODEX_HOME path is emitted without quoting, keeping the stable
    # command easy to recognize. shlex.quote handles homes containing spaces.
    return "/usr/bin/python3 " + shlex.quote(str(script))


def _group_matches(event: str, group: dict[str, Any]) -> bool:
    if event == "SessionStart":
        return group.get("matcher") == SESSION_START_MATCHER
    return "matcher" not in group


def _reconcile_event(groups: list[dict[str, Any]], event: str, command: str) -> bool:
    """Add one command to an existing compatible group or a new group."""

    compatible: dict[str, Any] | None = None
    for group in groups:
        if not _group_matches(event, group):
            continue
        if compatible is None:
            compatible = group
        if any(hook.get("command") == command for hook in group["hooks"]):
            return False
    timeout, status_message = HOOK_TIMEOUTS[event]
    command_hook = {
        "type": "command",
        "command": command,
        "timeout": timeout,
        "statusMessage": status_message,
    }
    if compatible is not None:
        compatible["hooks"].append(command_hook)
    elif event == "SessionStart":
        groups.append({"matcher": SESSION_START_MATCHER, "hooks": [command_hook]})
    else:
        groups.append({"hooks": [command_hook]})
    return True


def _atomic_write(path: Path, config: dict[str, Any], original: os.stat_result | None) -> None:
    """Write a mode-0600 JSON config and atomically replace the old file."""

    parent = path.parent
    try:
        fd, raw_temp = tempfile.mkstemp(prefix=f".{path.name}.", dir=parent)
    except OSError as exc:
        raise HookInstallError("cannot stage hooks.json safely") from exc
    temp_path = Path(raw_temp)
    try:
        os.fchmod(fd, HOOK_MODE)
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            fd = -1
            json.dump(config, stream, ensure_ascii=False, indent=2, allow_nan=False)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())

        # Do not overwrite a file that changed while this reconciliation was
        # reading it. A retry can merge the newer policy safely.
        _ensure_parent(path)
        current = _validate_existing(path, label="hooks.json")
        if original is None:
            if current is not None:
                raise HookInstallError("hooks.json changed while installing hooks; retry")
        elif current is None or any(
            getattr(current, field) != getattr(original, field)
            for field in ("st_ino", "st_dev", "st_size", "st_mtime_ns")
        ):
            raise HookInstallError("hooks.json changed while installing hooks; retry")

        os.replace(temp_path, path)
        os.chmod(path, HOOK_MODE, follow_symlinks=False)
        try:
            directory_fd = os.open(parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        except OSError:
            directory_fd = -1
        if directory_fd >= 0:
            try:
                os.fsync(directory_fd)
            finally:
                os.close(directory_fd)
    except HookInstallError:
        raise
    except (OSError, TypeError, ValueError) as exc:
        raise HookInstallError("cannot write hooks.json safely") from exc
    finally:
        if fd >= 0:
            os.close(fd)
        try:
            temp_path.unlink()
        except FileNotFoundError:
            pass
        except OSError:
            pass


def install_hooks(
    hooks_path: Path | str | None = None,
    codex_home_path: Path | str | None = None,
    *,
    codex_home: Path | str | None = None,
) -> HookInstallResult:
    """Add the TC lifecycle command to the Codex hook configuration.

    The operation is additive and idempotent. ``codex_home`` is accepted as a
    keyword alias because callers commonly use that name for the configured
    Codex home; ``codex_home_path`` remains convenient for positional tests.
    """

    if codex_home is not None:
        if codex_home_path is not None:
            raise TypeError("provide only one of codex_home_path and codex_home")
        codex_home_path = codex_home
    home = Path(codex_home_path).expanduser() if codex_home_path is not None else globals()["codex_home"]()
    home = home.absolute()
    path = Path(hooks_path).expanduser() if hooks_path is not None else home / "hooks.json"
    path = path.absolute()
    _ensure_parent(path)
    config, original = _read_config(path)
    hooks = _validate_relevant_shape(config)
    command = _hook_command(home)

    added: list[str] = []
    for event in HOOK_EVENTS:
        groups = hooks.get(event)
        if groups is None:
            groups = []
            hooks[event] = groups
        if _reconcile_event(groups, event, command):
            added.append(event)

    mode_changed = original is not None and stat.S_IMODE(original.st_mode) != HOOK_MODE
    changed = bool(added) or original is None or mode_changed
    if changed:
        _atomic_write(path, config, original)
    return HookInstallResult(path, changed, tuple(added), mode_changed)


def reconcile_hooks(
    hooks_path: Path | str | None = None,
    codex_home_path: Path | str | None = None,
    *,
    codex_home: Path | str | None = None,
) -> HookInstallResult:
    """Compatibility name for updater callers performing reconciliation."""

    return install_hooks(hooks_path, codex_home_path, codex_home=codex_home)


# Short aliases keep the helper convenient for callers that treat this file as
# a small library while the CLI remains the documented entry point.
reconcile = reconcile_hooks
install = install_hooks


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--hooks",
        "--hooks-path",
        dest="hooks_path",
        type=Path,
        help="hooks.json to reconcile (default: CODEX_HOME/hooks.json)",
    )
    parser.add_argument(
        "--codex-home",
        dest="codex_home_path",
        type=Path,
        help="Codex home used in the lifecycle command (default: CODEX_HOME)",
    )
    return parser


def main(argv: list[str] | None = None) -> int:
    try:
        args = build_parser().parse_args(argv)
        result = install_hooks(args.hooks_path, args.codex_home_path)
        if result.changed:
            message = (
                "hooks.json updated; review and trust the lifecycle hooks in Codex /hooks "
                "(trust hashes and config.toml were not changed)"
            )
        else:
            message = "no-op; TC Roadmap lifecycle hooks are already installed"
        json.dump(
            {
                "changed": result.changed,
                "hooks_path": str(result.hooks_path),
                "added_events": list(result.added_events),
                "review_required": result.review_required,
                "message": message,
            },
            sys.stdout,
            separators=(",", ":"),
        )
        sys.stdout.write("\n")
        return 0
    except (HookInstallError, OSError, TypeError, ValueError) as exc:
        print(f"tc-roadmap hooks: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
