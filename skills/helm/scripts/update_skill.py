#!/usr/bin/env python3
"""Install the latest Helm skill from its public GitHub repository."""

from __future__ import annotations

import argparse
import json
import os
import shutil
import stat
import string
import subprocess
import sys
import tempfile
from pathlib import Path

try:
    from install_hooks import install_hooks
except ImportError:  # pragma: no cover - retained for older partial installs
    install_hooks = None  # type: ignore[assignment]


DEFAULT_REPOSITORY = "https://github.com/KanterLabs/helm.git"
SKILL_SUBDIRECTORY = Path("skills/helm")
CANONICAL_SKILL_NAME = "helm"
LEGACY_SKILL_NAME = "tc-roadmap"


class UpdateError(RuntimeError):
    """A safe-to-display updater error."""


def codex_home() -> Path:
    configured = os.environ.get("CODEX_HOME", "").strip()
    return Path(configured).expanduser() if configured else Path.home() / ".codex"


def _run(command: list[str], *, cwd: Path | None = None) -> str:
    try:
        result = subprocess.run(
            command,
            cwd=cwd,
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=60,
        )
    except FileNotFoundError as exc:
        raise UpdateError("git is required to update the Helm skill") from exc
    except subprocess.TimeoutExpired as exc:
        raise UpdateError("GitHub skill update timed out") from exc
    except subprocess.CalledProcessError as exc:
        raise UpdateError("GitHub skill update failed") from exc
    return result.stdout.strip()


def _validate_source(source: Path) -> None:
    skill = source / "SKILL.md"
    helper = source / "scripts" / "helm.py"
    if not skill.is_file() or not helper.is_file():
        raise UpdateError("GitHub checkout does not contain the expected Helm skill")
    header = skill.read_text(encoding="utf-8")[:4096]
    if "name: helm" not in header:
        raise UpdateError("GitHub checkout contains an invalid Helm skill")


def _read_installed_revision(target: Path) -> str:
    marker = target / ".source-revision"
    if target.is_symlink() or marker.is_symlink():
        return ""
    try:
        _validate_source(target)
    except (UpdateError, OSError, UnicodeError):
        return ""
    try:
        revision = marker.read_text(encoding="utf-8").strip().lower()
    except (OSError, UnicodeError):
        return ""
    if len(revision) not in {40, 64} or any(character not in string.hexdigits for character in revision):
        return ""
    return revision


def _prepare_install_parent(parent: Path) -> None:
    """Create an owned install directory without following symlinked parents."""

    parent = parent.expanduser().absolute()
    missing: list[Path] = []
    current = parent
    while True:
        try:
            info = current.lstat()
        except FileNotFoundError:
            missing.append(current)
        except OSError as exc:
            raise UpdateError("cannot inspect the skill install path") from exc
        else:
            if stat.S_ISLNK(info.st_mode):
                raise UpdateError("refusing to use a symlink in the skill install path")
            if not stat.S_ISDIR(info.st_mode):
                raise UpdateError("skill install parent is not a directory")
        if current.parent == current:
            break
        current = current.parent

    for directory in reversed(missing):
        try:
            directory.mkdir()
        except FileExistsError:
            pass
        except OSError as exc:
            raise UpdateError("cannot create the skill install path") from exc

    # Recheck after creation to close a missing-component replacement race.
    current = parent
    while True:
        try:
            info = current.lstat()
        except OSError as exc:
            raise UpdateError("cannot inspect the skill install path") from exc
        if stat.S_ISLNK(info.st_mode):
            raise UpdateError("refusing to use a symlink in the skill install path")
        if not stat.S_ISDIR(info.st_mode):
            raise UpdateError("skill install parent is not a directory")
        if current.parent == current:
            break
        current = current.parent
    if parent.stat().st_uid != os.getuid():
        raise UpdateError("skill install parent is not owned by the current user")


def _remote_revision(repository: str) -> str:
    output = _run(["git", "ls-remote", "--exit-code", "--", repository, "refs/heads/main"])
    lines = [line.split() for line in output.splitlines() if line.strip()]
    matches = [parts[0].lower() for parts in lines if len(parts) == 2 and parts[1] == "refs/heads/main"]
    if len(matches) != 1 or len(matches[0]) not in {40, 64} or any(
        character not in string.hexdigits for character in matches[0]
    ):
        raise UpdateError("GitHub returned an invalid main revision")
    return matches[0]


def _canonical_target(target: Path) -> Path:
    """Map an explicitly supplied legacy target to the canonical sibling."""

    target = target.expanduser()
    if target.name == LEGACY_SKILL_NAME and target.parent.name == "skills":
        return target.with_name(CANONICAL_SKILL_NAME)
    return target


def install_from_source(source: Path, target: Path, *, revision: str = "") -> None:
    _validate_source(source)
    if revision and (
        len(revision) not in {40, 64} or any(character not in string.hexdigits for character in revision)
    ):
        raise UpdateError("refusing to record an invalid source revision")
    target = _canonical_target(target).absolute()
    parent = target.parent
    if target.name != CANONICAL_SKILL_NAME or parent.name != "skills":
        raise UpdateError("refusing to replace an unexpected skill target")
    _prepare_install_parent(parent)
    if target.is_symlink():
        raise UpdateError("refusing to replace a symlinked skill target")
    with tempfile.TemporaryDirectory(prefix=".helm-stage-", dir=parent) as raw_stage:
        stage_root = Path(raw_stage)
        stage = stage_root / "helm"
        shutil.copytree(source, stage)
        if revision:
            (stage / ".source-revision").write_text(revision + "\n", encoding="utf-8")
        backup = stage_root / "previous"
        replaced = False
        try:
            if target.exists() or target.is_symlink():
                target.rename(backup)
                replaced = True
            stage.rename(target)
        except Exception:
            if replaced and backup.exists() and not target.exists():
                backup.rename(target)
            raise


def _hooks_home_for_target(target: Path) -> Path:
    """Resolve the effective Codex home without mutating a test/user home."""

    if os.environ.get("CODEX_HOME", "").strip():
        return codex_home().absolute()
    if target.parent.name == "skills":
        return target.parent.parent.absolute()
    return codex_home().absolute()


def reconcile_hooks(target: Path) -> object:
    """Reconcile local lifecycle hooks, independent of GitHub fetching."""

    if install_hooks is None:
        raise UpdateError("the installed Helm skill has no lifecycle hook installer")
    home = _hooks_home_for_target(target)
    return install_hooks(home / "hooks.json", home)


def _reconcile_hooks_safely(target: Path) -> None:
    """Keep skill updates successful when local hook reconciliation fails."""

    try:
        result = reconcile_hooks(target)
    except Exception:
        # Hook installation is intentionally best effort. The installer itself
        # validates and atomically writes hooks.json, so a failure leaves both
        # the existing skill and existing hook policy intact.
        print(
            "helm update: warning: lifecycle hooks were not reconciled; "
            "the skill result is still valid; review hooks.json manually",
            file=sys.stderr,
        )
        return
    if getattr(result, "changed", False) is True:
        print(
            "helm update: lifecycle hooks changed; review and trust them in Codex /hooks "
            "(trust hashes and config.toml were not changed)",
            file=sys.stderr,
        )


def update(repository: str, target: Path) -> tuple[bool, str]:
    if repository != DEFAULT_REPOSITORY:
        raise UpdateError("refusing to update from an untrusted repository")
    target = _canonical_target(target)
    remote_revision = _remote_revision(repository)
    if _read_installed_revision(target) == remote_revision:
        _reconcile_hooks_safely(target)
        return False, remote_revision
    with tempfile.TemporaryDirectory(prefix="helm-fetch-") as raw_checkout:
        checkout = Path(raw_checkout) / "repo"
        _run(
            [
                "git",
                "clone",
                "--quiet",
                "--depth",
                "1",
                "--filter=blob:none",
                "--sparse",
                "--branch",
                "main",
                "--",
                repository,
                str(checkout),
            ]
        )
        _run(["git", "sparse-checkout", "set", "--", str(SKILL_SUBDIRECTORY)], cwd=checkout)
        revision = _run(["git", "rev-parse", "HEAD"], cwd=checkout)
        install_from_source(checkout / SKILL_SUBDIRECTORY, target, revision=revision)
        _reconcile_hooks_safely(target)
        return True, revision


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", default=DEFAULT_REPOSITORY)
    parser.add_argument("--target", type=Path, default=codex_home() / "skills" / "helm")
    return parser


def main() -> int:
    try:
        args = build_parser().parse_args()
        updated, revision = update(args.repository, args.target.expanduser())
        json.dump({"updated": updated, "revision": revision}, sys.stdout, separators=(",", ":"))
        sys.stdout.write("\n")
        return 0
    except (UpdateError, OSError) as exc:
        print(f"helm update: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
