#!/usr/bin/env python3
"""Unit tests for the TC Roadmap skill updater."""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

import update_skill


class InstallTests(unittest.TestCase):
    def test_install_replaces_only_expected_skill_target(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            source = root / "source"
            (source / "scripts").mkdir(parents=True)
            (source / "SKILL.md").write_text("---\nname: tc-roadmap\n---\n", encoding="utf-8")
            (source / "scripts" / "tc_roadmap.py").write_text("# helper\n", encoding="utf-8")
            target = root / "skills" / "tc-roadmap"
            target.mkdir(parents=True)
            (target / "old.txt").write_text("old\n", encoding="utf-8")

            update_skill.install_from_source(source, target)

            self.assertTrue((target / "SKILL.md").is_file())
            self.assertFalse((target / "old.txt").exists())

    def test_install_rejects_unexpected_target(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            source = root / "source"
            (source / "scripts").mkdir(parents=True)
            (source / "SKILL.md").write_text("---\nname: tc-roadmap\n---\n", encoding="utf-8")
            (source / "scripts" / "tc_roadmap.py").write_text("# helper\n", encoding="utf-8")
            with self.assertRaisesRegex(update_skill.UpdateError, "unexpected skill target"):
                update_skill.install_from_source(source, root / "wrong")


if __name__ == "__main__":
    unittest.main(verbosity=2)
