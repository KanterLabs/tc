#!/usr/bin/env python3
"""Unit tests for the additive TC Roadmap lifecycle hook installer."""

from __future__ import annotations

import json
import os
import stat
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import install_hooks


class HookInstallerTests(unittest.TestCase):
    def _paths(self, raw: str) -> tuple[Path, Path]:
        root = Path(raw)
        return root / "codex", root / "codex" / "hooks.json"

    def _command(self, home: Path) -> str:
        return f"/usr/bin/python3 {home / 'skills/tc-roadmap/scripts/tc_roadmap_hook.py'}"

    def test_empty_new_config_adds_every_event_and_mode_0600(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            home, hooks_path = self._paths(raw)
            result = install_hooks.install_hooks(hooks_path, home)

            self.assertTrue(result.changed)
            self.assertEqual(result.added_events, install_hooks.HOOK_EVENTS)
            self.assertTrue(result.review_required)
            self.assertEqual(stat.S_IMODE(hooks_path.stat().st_mode), 0o600)
            data = json.loads(hooks_path.read_text(encoding="utf-8"))
            command = self._command(home)
            self.assertEqual(data["hooks"]["SessionStart"][0]["matcher"], install_hooks.SESSION_START_MATCHER)
            for event in install_hooks.HOOK_EVENTS:
                groups = data["hooks"][event]
                self.assertEqual(len(groups), 1)
                self.assertEqual(
                    groups[0]["hooks"],
                    [
                        {
                            "type": "command",
                            "command": command,
                            "timeout": install_hooks.HOOK_TIMEOUTS[event][0],
                            "statusMessage": install_hooks.HOOK_TIMEOUTS[event][1],
                        }
                    ],
                )

    def test_merges_global_and_linkshare_policy_without_replacing_commands(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            home, hooks_path = self._paths(raw)
            existing = {
                "description": "Global Codex session policies and intentional Linkshare handoff.",
                "groups": {"linkshare": {"enabled": True}},
                "custom": {"keep": [1, 2, 3]},
                "hooks": {
                    "SessionStart": [
                        {
                            "matcher": install_hooks.SESSION_START_MATCHER,
                            "hooks": [
                                {
                                    "type": "command",
                                    "command": "/usr/bin/python3 /home/shane/.codex/hooks/global_session_policy.py",
                                    "timeout": 5,
                                }
                            ],
                        }
                    ],
                    "Stop": [
                        {
                            "hooks": [
                                {
                                    "type": "command",
                                    "command": "/usr/bin/python3 /home/shane/.codex/hooks/linkshare_handoff.py",
                                    "timeout": 30,
                                }
                            ]
                        }
                    ],
                    "PostToolUse": [
                        {"matcher": "Write|Edit", "hooks": [{"type": "command", "command": "./policy-check"}]}
                    ],
                    "OtherEvent": [{"hooks": [{"type": "command", "command": "./unrelated"}]}],
                },
            }
            hooks_path.parent.mkdir(parents=True)
            hooks_path.write_text(json.dumps(existing, indent=2) + "\n", encoding="utf-8")
            hooks_path.chmod(0o600)

            result = install_hooks.install_hooks(hooks_path, home)
            data = json.loads(hooks_path.read_text(encoding="utf-8"))
            command = self._command(home)

            self.assertTrue(result.changed)
            self.assertEqual(data["description"], existing["description"])
            self.assertEqual(data["groups"], existing["groups"])
            self.assertEqual(data["custom"], existing["custom"])
            self.assertEqual(data["hooks"]["OtherEvent"], existing["hooks"]["OtherEvent"])
            self.assertIn("/home/shane/.codex/hooks/global_session_policy.py", json.dumps(data))
            self.assertIn("/home/shane/.codex/hooks/linkshare_handoff.py", json.dumps(data))
            for event in install_hooks.HOOK_EVENTS:
                self.assertEqual(
                    sum(hook.get("command") == command for group in data["hooks"][event] for hook in group["hooks"]),
                    1,
                )
            self.assertEqual(data["hooks"]["PostToolUse"][0]["hooks"][0]["command"], "./policy-check")

    def test_second_run_is_idempotent_and_does_not_rewrite(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            home, hooks_path = self._paths(raw)
            first = install_hooks.install_hooks(hooks_path, home)
            before = hooks_path.read_bytes()
            inode = hooks_path.stat().st_ino
            second = install_hooks.install_hooks(hooks_path, home)

            self.assertTrue(first.changed)
            self.assertFalse(second.changed)
            self.assertEqual(second.added_events, ())
            self.assertFalse(second.mode_changed)
            self.assertEqual(hooks_path.read_bytes(), before)
            self.assertEqual(hooks_path.stat().st_ino, inode)

    def test_existing_command_is_deduped_without_mutating_its_metadata(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            home, hooks_path = self._paths(raw)
            command = self._command(home)
            existing_hook = {
                "type": "command",
                "command": command,
                "timeout": 99,
                "statusMessage": "Policy-owned status",
                "custom": {"keep": True},
            }
            config = {
                "hooks": {
                    "SessionStart": [
                        {"matcher": install_hooks.SESSION_START_MATCHER, "hooks": [existing_hook]}
                    ]
                }
            }
            hooks_path.parent.mkdir(parents=True)
            hooks_path.write_text(json.dumps(config) + "\n", encoding="utf-8")
            hooks_path.chmod(0o600)

            result = install_hooks.install_hooks(hooks_path, home)
            data = json.loads(hooks_path.read_text(encoding="utf-8"))

            self.assertNotIn("SessionStart", result.added_events)
            self.assertEqual(data["hooks"]["SessionStart"][0]["hooks"][0], existing_hook)
            for event in ("PostToolUse", "PreCompact", "Stop"):
                hook = data["hooks"][event][0]["hooks"][0]
                self.assertEqual(hook["timeout"], install_hooks.HOOK_TIMEOUTS[event][0])
                self.assertEqual(hook["statusMessage"], install_hooks.HOOK_TIMEOUTS[event][1])

    def test_rejects_symlink_without_touching_target(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            home = root / "codex"
            real = root / "real-hooks.json"
            real.write_text('{"keep": true}\n', encoding="utf-8")
            real.chmod(0o600)
            linked = home / "hooks.json"
            linked.parent.mkdir()
            linked.symlink_to(real)

            with self.assertRaisesRegex(install_hooks.HookInstallError, "symlink"):
                install_hooks.install_hooks(linked, home)
            self.assertEqual(real.read_text(encoding="utf-8"), '{"keep": true}\n')

    def test_rejects_symlinked_parent_component(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            home = root / "codex"
            real_parent = root / "real-codex"
            real_parent.mkdir()
            home.symlink_to(real_parent, target_is_directory=True)
            with self.assertRaisesRegex(install_hooks.HookInstallError, "symlink"):
                install_hooks.install_hooks(home / "hooks.json", home)

    def test_rejects_foreign_owner_without_writing(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            home, hooks_path = self._paths(raw)
            hooks_path.parent.mkdir(parents=True)
            hooks_path.write_text('{"keep": true}\n', encoding="utf-8")
            hooks_path.chmod(0o600)
            before = hooks_path.read_bytes()
            with mock.patch.object(install_hooks.os, "getuid", return_value=os.getuid() + 1):
                with self.assertRaisesRegex(install_hooks.HookInstallError, "not owned"):
                    install_hooks.install_hooks(hooks_path, home)
            self.assertEqual(hooks_path.read_bytes(), before)

    def test_rejects_bad_json_without_writing(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            home, hooks_path = self._paths(raw)
            hooks_path.parent.mkdir(parents=True)
            hooks_path.write_text('{"hooks":', encoding="utf-8")
            hooks_path.chmod(0o600)
            before = hooks_path.read_bytes()
            with self.assertRaisesRegex(install_hooks.HookInstallError, "valid JSON"):
                install_hooks.install_hooks(hooks_path, home)
            self.assertEqual(hooks_path.read_bytes(), before)

    def test_atomic_replace_and_result_mode_are_safe(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            home, hooks_path = self._paths(raw)
            hooks_path.parent.mkdir(parents=True)
            hooks_path.write_text("{}\n", encoding="utf-8")
            hooks_path.chmod(0o644)
            calls: list[tuple[Path, Path]] = []
            replace = install_hooks.os.replace

            def record_replace(source: str | os.PathLike[str], target: str | os.PathLike[str]) -> None:
                calls.append((Path(source), Path(target)))
                replace(source, target)

            with mock.patch.object(install_hooks.os, "replace", side_effect=record_replace):
                result = install_hooks.install_hooks(hooks_path, home)

            self.assertTrue(result.changed)
            self.assertEqual(calls[0][1], hooks_path)
            self.assertNotEqual(calls[0][0], hooks_path)
            self.assertEqual(stat.S_IMODE(hooks_path.stat().st_mode), 0o600)
            self.assertEqual(list(hooks_path.parent.glob(f".{hooks_path.name}.*")), [])

    def test_cli_reports_review_on_change_and_noop_on_second_run(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            home, hooks_path = self._paths(raw)
            with mock.patch.object(
                install_hooks.sys,
                "argv",
                ["install_hooks.py", "--hooks", str(hooks_path), "--codex-home", str(home)],
            ), mock.patch.object(install_hooks.sys, "stdout") as stdout:
                self.assertEqual(install_hooks.main(), 0)
                changed = json.loads("".join(call.args[0] for call in stdout.write.call_args_list))
            self.assertTrue(changed["changed"])
            self.assertTrue(changed["review_required"])
            self.assertIn("/hooks", changed["message"])

            with mock.patch.object(
                install_hooks.sys,
                "argv",
                ["install_hooks.py", "--hooks", str(hooks_path), "--codex-home", str(home)],
            ), mock.patch.object(install_hooks.sys, "stdout") as stdout:
                self.assertEqual(install_hooks.main(), 0)
                noop = json.loads("".join(call.args[0] for call in stdout.write.call_args_list))
            self.assertFalse(noop["changed"])
            self.assertIn("no-op", noop["message"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
