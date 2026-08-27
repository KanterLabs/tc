#!/usr/bin/env python3
"""Install the latest TC Roadmap skill from its public GitHub repository."""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path


DEFAULT_REPOSITORY = "https://github.com/KanterLabs/tc.git"
SKILL_SUBDIRECTORY = Path("skills/tc-roadmap")


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
        raise UpdateError("git is required to update the TC Roadmap skill") from exc
    except subprocess.TimeoutExpired as exc:
        raise UpdateError("GitHub skill update timed out") from exc
    except subprocess.CalledProcessError as exc:
        raise UpdateError("GitHub skill update failed") from exc
    return result.stdout.strip()


def _validate_source(source: Path) -> None:
    skill = source / "SKILL.md"
    helper = source / "scripts" / "tc_roadmap.py"
    if not skill.is_file() or not helper.is_file():
        raise UpdateError("GitHub checkout does not contain the expected TC Roadmap skill")
    header = skill.read_text(encoding="utf-8")[:4096]
    if "name: tc-roadmap" not in header:
        raise UpdateError("GitHub checkout contains an invalid TC Roadmap skill")


def install_from_source(source: Path, target: Path) -> None:
    _validate_source(source)
    parent = target.parent
    parent.mkdir(parents=True, exist_ok=True)
    if target.name != "tc-roadmap" or parent.name != "skills":
        raise UpdateError("refusing to replace an unexpected skill target")
    with tempfile.TemporaryDirectory(prefix=".tc-roadmap-stage-", dir=parent) as raw_stage:
        stage_root = Path(raw_stage)
        stage = stage_root / "tc-roadmap"
        shutil.copytree(source, stage)
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


def update(repository: str, target: Path) -> str:
    if repository != DEFAULT_REPOSITORY:
        raise UpdateError("refusing to update from an untrusted repository")
    with tempfile.TemporaryDirectory(prefix="tc-roadmap-fetch-") as raw_checkout:
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
        install_from_source(checkout / SKILL_SUBDIRECTORY, target)
        return revision


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", default=DEFAULT_REPOSITORY)
    parser.add_argument("--target", type=Path, default=codex_home() / "skills" / "tc-roadmap")
    return parser


def main() -> int:
    try:
        args = build_parser().parse_args()
        revision = update(args.repository, args.target.expanduser())
        json.dump({"updated": True, "revision": revision}, sys.stdout, separators=(",", ":"))
        sys.stdout.write("\n")
        return 0
    except (UpdateError, OSError) as exc:
        print(f"tc-roadmap update: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
