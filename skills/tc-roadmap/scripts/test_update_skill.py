#!/usr/bin/env python3
"""Unit tests for the Helm skill updater."""

from __future__ import annotations

import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import update_skill


class InstallTests(unittest.TestCase):
    def test_legacy_updater_defaults_to_canonical_target_and_repository(self) -> None:
        args = update_skill.build_parser().parse_args([])
        self.assertEqual(update_skill.DEFAULT_REPOSITORY, "https://github.com/KanterLabs/helm.git")
        self.assertEqual(update_skill.SKILL_SUBDIRECTORY, Path("skills/helm"))
        self.assertEqual(args.target.name, "helm")

    def test_install_replaces_only_expected_skill_target(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            source = root / "source"
            (source / "scripts").mkdir(parents=True)
            (source / "SKILL.md").write_text("---\nname: helm\n---\n", encoding="utf-8")
            (source / "scripts" / "helm.py").write_text("# helper\n", encoding="utf-8")
            target = root / "skills" / "helm"
            target.mkdir(parents=True)
            (target / "old.txt").write_text("old\n", encoding="utf-8")

            update_skill.install_from_source(source, target)

            self.assertTrue((target / "SKILL.md").is_file())
            self.assertFalse((target / "old.txt").exists())

    def test_install_records_source_revision(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            source = root / "source"
            (source / "scripts").mkdir(parents=True)
            (source / "SKILL.md").write_text("---\nname: helm\n---\n", encoding="utf-8")
            (source / "scripts" / "helm.py").write_text("# helper\n", encoding="utf-8")
            target = root / "skills" / "helm"

            update_skill.install_from_source(source, target, revision="a" * 40)

            self.assertEqual((target / ".source-revision").read_text(encoding="utf-8"), "a" * 40 + "\n")

    def test_legacy_target_installs_canonical_sibling_without_removing_legacy(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            source = root / "source"
            (source / "scripts").mkdir(parents=True)
            (source / "SKILL.md").write_text("---\nname: helm\n---\n", encoding="utf-8")
            (source / "scripts" / "helm.py").write_text("# helper\n", encoding="utf-8")
            legacy = root / "skills" / "tc-roadmap"
            legacy.mkdir(parents=True)
            (legacy / "keep.txt").write_text("legacy\n", encoding="utf-8")

            update_skill.install_from_source(source, legacy, revision="a" * 40)

            self.assertEqual((legacy / "keep.txt").read_text(encoding="utf-8"), "legacy\n")
            canonical = root / "skills" / "helm"
            self.assertTrue((canonical / "SKILL.md").is_file())
            self.assertEqual((canonical / ".source-revision").read_text(encoding="utf-8"), "a" * 40 + "\n")

    def test_install_rejects_unexpected_target(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            source = root / "source"
            (source / "scripts").mkdir(parents=True)
            (source / "SKILL.md").write_text("---\nname: helm\n---\n", encoding="utf-8")
            (source / "scripts" / "helm.py").write_text("# helper\n", encoding="utf-8")
            with self.assertRaisesRegex(update_skill.UpdateError, "unexpected skill target"):
                update_skill.install_from_source(source, root / "wrong")

    def test_install_rejects_symlinked_parent_without_touching_target(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            source = root / "source"
            (source / "scripts").mkdir(parents=True)
            (source / "SKILL.md").write_text("---\nname: helm\n---\n", encoding="utf-8")
            (source / "scripts" / "helm.py").write_text("# helper\n", encoding="utf-8")
            real = root / "real-skills"
            real.mkdir()
            (real / "keep.txt").write_text("keep\n", encoding="utf-8")
            linked = root / "codex" / "skills"
            linked.parent.mkdir()
            linked.symlink_to(real, target_is_directory=True)

            with self.assertRaisesRegex(update_skill.UpdateError, "symlink"):
                update_skill.install_from_source(source, linked / "helm")
            self.assertEqual((real / "keep.txt").read_text(encoding="utf-8"), "keep\n")
            self.assertFalse((real / "helm").exists())

    def test_update_skips_clone_when_installed_revision_matches(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            target = Path(raw) / "skills" / "helm"
            (target / "scripts").mkdir(parents=True)
            (target / "SKILL.md").write_text("---\nname: helm\n---\n", encoding="utf-8")
            (target / "scripts" / "helm.py").write_text("# helper\n", encoding="utf-8")
            (target / ".source-revision").write_text("a" * 40 + "\n", encoding="utf-8")
            with mock.patch.object(update_skill, "_remote_revision", return_value="a" * 40), mock.patch.object(
                update_skill, "_run"
            ) as run, mock.patch.object(update_skill, "reconcile_hooks") as reconcile:
                updated, revision = update_skill.update(update_skill.DEFAULT_REPOSITORY, target)

            self.assertFalse(updated)
            self.assertEqual(revision, "a" * 40)
            run.assert_not_called()
            reconcile.assert_called_once_with(target)

    def test_legacy_target_noop_still_reconciles_canonical_hooks(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            target = root / "skills" / "helm"
            (target / "scripts").mkdir(parents=True)
            (target / "SKILL.md").write_text("---\nname: helm\n---\n", encoding="utf-8")
            (target / "scripts" / "helm.py").write_text("# helper\n", encoding="utf-8")
            (target / ".source-revision").write_text("a" * 40 + "\n", encoding="utf-8")
            legacy_target = root / "skills" / "tc-roadmap"
            legacy_target.mkdir()
            with mock.patch.object(update_skill, "_remote_revision", return_value="a" * 40), mock.patch.object(
                update_skill, "_run"
            ) as run, mock.patch.object(update_skill, "reconcile_hooks") as reconcile:
                updated, revision = update_skill.update(update_skill.DEFAULT_REPOSITORY, legacy_target)

            self.assertFalse(updated)
            self.assertEqual(revision, "a" * 40)
            run.assert_not_called()
            reconcile.assert_called_once_with(target)

    def test_update_clones_and_installs_when_revision_differs(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            target = Path(raw) / "skills" / "helm"
            target.mkdir(parents=True)
            (target / ".source-revision").write_text("a" * 40 + "\n", encoding="utf-8")

            def run(command: list[str], *, cwd: Path | None = None) -> str:
                if command[:2] == ["git", "rev-parse"]:
                    return "b" * 40
                return ""

            with mock.patch.object(update_skill, "_remote_revision", return_value="b" * 40), mock.patch.object(
                update_skill, "_run", side_effect=run
            ) as execute, mock.patch.object(update_skill, "install_from_source") as install, mock.patch.object(
                update_skill, "reconcile_hooks"
            ) as reconcile:
                updated, revision = update_skill.update(update_skill.DEFAULT_REPOSITORY, target)

            self.assertTrue(updated)
            self.assertEqual(revision, "b" * 40)
            self.assertTrue(any(call.args[0][:2] == ["git", "clone"] for call in execute.call_args_list))
            install.assert_called_once()
            self.assertEqual(install.call_args.kwargs["revision"], "b" * 40)
            reconcile.assert_called_once_with(target)

    def test_hook_reconciliation_failure_warns_without_failing_skill_update(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            target = Path(raw) / "skills" / "helm"
            target.mkdir(parents=True)
            (target / ".source-revision").write_text("a" * 40 + "\n", encoding="utf-8")

            def run(command: list[str], *, cwd: Path | None = None) -> str:
                if command[:2] == ["git", "rev-parse"]:
                    return "b" * 40
                return ""

            stderr = io.StringIO()
            with mock.patch.object(update_skill, "_remote_revision", return_value="b" * 40), mock.patch.object(
                update_skill, "_run", side_effect=run
            ), mock.patch.object(update_skill, "install_from_source"), mock.patch.object(
                update_skill, "reconcile_hooks", side_effect=RuntimeError("not safe to display")
            ), contextlib.redirect_stderr(stderr):
                updated, revision = update_skill.update(update_skill.DEFAULT_REPOSITORY, target)

            self.assertTrue(updated)
            self.assertEqual(revision, "b" * 40)
            self.assertIn("warning", stderr.getvalue())
            self.assertNotIn("not safe to display", stderr.getvalue())

    def test_invalid_marker_requires_fetch(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            target = Path(raw) / "skills" / "helm"
            target.mkdir(parents=True)
            (target / ".source-revision").write_text("not-a-revision\n", encoding="utf-8")
            self.assertEqual(update_skill._read_installed_revision(target), "")

    def test_valid_marker_does_not_hide_invalid_or_linked_install(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            invalid = root / "skills" / "helm"
            invalid.mkdir(parents=True)
            (invalid / ".source-revision").write_text("a" * 40 + "\n", encoding="utf-8")
            self.assertEqual(update_skill._read_installed_revision(invalid), "")

            linked = root / "linked"
            linked.symlink_to(invalid, target_is_directory=True)
            self.assertEqual(update_skill._read_installed_revision(linked), "")

    def test_remote_revision_rejects_unexpected_response(self) -> None:
        with mock.patch.object(update_skill, "_run", return_value="not-a-revision\trefs/heads/main"):
            with self.assertRaisesRegex(update_skill.UpdateError, "invalid main revision"):
                update_skill._remote_revision(update_skill.DEFAULT_REPOSITORY)

    def test_cli_reports_noop_and_update_states(self) -> None:
        for updated in (False, True):
            with self.subTest(updated=updated), mock.patch.object(
                update_skill, "update", return_value=(updated, "a" * 40)
            ), mock.patch.object(update_skill.sys, "argv", ["update_skill.py"]), mock.patch.object(
                update_skill.sys, "stdout", io.StringIO()
            ) as stdout:
                self.assertEqual(update_skill.main(), 0)
                self.assertEqual(json.loads(stdout.getvalue()), {"updated": updated, "revision": "a" * 40})


if __name__ == "__main__":
    unittest.main(verbosity=2)
