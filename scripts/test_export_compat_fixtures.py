import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "export_compat_fixtures", Path(__file__).with_name("export_compat_fixtures.py"))
EXPORT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EXPORT)


class ExportFixturesTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repository"
        self.repo.mkdir()
        self.git("init", "-q")
        self.git("config", "user.name", "Test")
        self.git("config", "user.email", "test@example.invalid")
        for component in ("Application", "Domain"):
            path = self.repo / "gitlab-ai" / "src" / ("Petrel.GitLabAi." + component)
            path.mkdir(parents=True)
            (path / "Source.cs").write_text("pinned source\n")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        self.baseline = self.git("rev-parse", "HEAD").strip()

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.repo), *args], check=True,
                              capture_output=True, text=True).stdout

    def test_archive_uses_pinned_content_and_preserves_source(self):
        source = self.repo / "gitlab-ai" / "src" / "Petrel.GitLabAi.Application" / "Source.cs"
        source.write_text("dirty source\n")
        (source.parent / "obj").mkdir()
        (source.parent / "obj" / "keep").write_text("existing object\n")
        before = self.git("status", "--porcelain=v1")
        copied = EXPORT.isolated_source(self.repo / "gitlab-ai", self.root / "isolated", self.baseline)
        self.assertEqual((copied / "src" / "Petrel.GitLabAi.Application" / "Source.cs").read_text(), "pinned source\n")
        self.assertFalse((copied / "src" / "Petrel.GitLabAi.Application" / "obj").exists())
        self.assertEqual(source.read_text(), "dirty source\n")
        self.assertEqual((source.parent / "obj" / "keep").read_text(), "existing object\n")
        self.assertEqual(self.git("status", "--porcelain=v1"), before)

    def test_generation_builds_only_the_isolated_copy(self):
        original = subprocess.run
        calls = []

        def run(command, **kwargs):
            if command[0] != "dotnet":
                return original(command, **kwargs)
            calls.append(command)
            selected = next(arg for arg in command if arg.startswith("--property:MarinaSource="))
            self.assertFalse(Path(selected.split("=", 1)[1]).is_relative_to(self.repo))
            Path(command[-1]).write_text('{"syntheticOnly":true}\n')
            return subprocess.CompletedProcess(command, 0)

        with patch.object(EXPORT.subprocess, "run", side_effect=run):
            manifest = EXPORT.generate(self.repo, self.root / "fixtures", self.baseline)
        self.assertEqual(len(calls), 1)
        self.assertEqual(manifest["baseline"], self.baseline)
        self.assertTrue(manifest["syntheticOnly"])
        self.assertEqual(len(manifest["sourceHashes"]), 2)
        self.assertEqual(json.loads((self.root / "fixtures" / "stages.json").read_text()), {"syntheticOnly": True})


if __name__ == "__main__":
    unittest.main()
