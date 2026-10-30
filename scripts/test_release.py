import importlib.util
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch


def module(name):
    spec = importlib.util.spec_from_file_location(name, pathlib.Path(__file__).with_name(name + ".py"))
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


class ReleaseTests(unittest.TestCase):
    def test_version_is_strict_and_archive_names_are_fixed(self):
        release = module("release")
        for value in ("0.1.0", "12.3.4"):
            self.assertEqual(release.validate_version(value), value)
        for value in ("v0.1.0", "../version", "1.0", "01.2.3", "1.2.3;touch x"):
            with self.assertRaises(ValueError):
                release.validate_version(value)

    def test_formula_has_verified_hash_and_correct_binary(self):
        brew = module("render_homebrew")
        text = brew.render("0.1.0", "a" * 40, "b" * 64)
        self.assertIn("class MarinaTheReviewer < Formula", text)
        self.assertIn("marinadoreview", text)
        self.assertIn("b" * 64, text)
        self.assertNotIn("no_check", text)
        for version, commit, digest in (("1.0;bad", "a" * 40, "b" * 64), ("0.1.0", "bad", "b" * 64), ("0.1.0", "a" * 40, "bad")):
            with self.assertRaises(ValueError):
                brew.render(version, commit, digest)

    def test_release_plan_has_no_mutating_commands(self):
        release = module("release")
        actions = release.release_plan("0.1.0")
        self.assertEqual(actions[-1], ["git", "push", "origin", "v0.1.0"])
        self.assertFalse(any("commit" in command or "checkout" in command or "add" in command for command in actions))

    def test_only_a_complete_published_release_can_be_reused(self):
        release = module("release")
        names = ["checksums.txt", "marina-the-reviewer_0.1.1_source.tar.gz"]
        names += [f"marina-the-reviewer_0.1.1_{system}_{arch}.tar.gz" for system in ("darwin", "linux") for arch in ("arm64", "amd64")]
        value = {"tag_name": "v0.1.1", "draft": False, "prerelease": False, "assets": [{"name": name, "size": 1} for name in names]}
        self.assertTrue(release.release_complete(value, "0.1.1"))
        for change in ({"draft": True}, {"prerelease": True}, {"tag_name": "v0.1.0"}, {"assets": value["assets"][:-1]}, {"assets": value["assets"] + [value["assets"][0]]}):
            with self.subTest(change=change):
                self.assertFalse(release.release_complete(dict(value, **change), "0.1.1"))

    def test_ci_requires_the_exact_main_or_tag_event(self):
        release = module("release")
        state = {"tag": "v0.1.1", "commit": "a" * 40}
        env = {"GITHUB_ACTIONS": "true", "GITHUB_REF": "refs/heads/main", "GITHUB_SHA": "a" * 40, "GITHUB_OUTPUT": "outputs"}
        with patch.dict(release.os.environ, env, clear=True):
            release.ci_context(state)
        for change in ({"GITHUB_REF": "refs/heads/dev"}, {"GITHUB_SHA": "b" * 40}, {"GITHUB_REF": "refs/tags/v0.1.0"}, {"GITHUB_ACTIONS": "false"}):
            with patch.dict(release.os.environ, dict(env, **change), clear=True):
                with self.assertRaises(ValueError):
                    release.ci_context(state)


class ReleaseGitTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = pathlib.Path(self.temp.name)
        self.git("init", "--initial-branch=main")
        self.git("config", "user.name", "Test")
        self.git("config", "user.email", "test@example.invalid")
        (self.repo / "VERSION").write_text("0.1.0\n")
        self.commit("base")
        self.git("tag", "v0.1.0")
        (self.repo / "VERSION").write_text("0.1.1\n")
        self.commit("new version")
        self.release = module("release")

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.repo, check=True, text=True, capture_output=True).stdout.strip()

    def commit(self, message):
        self.git("add", ".")
        self.git("commit", "-m", message)

    def state(self):
        return self.release.release_state(self.repo, main_ref="main")

    def test_uses_version_file_without_incrementing_it(self):
        value = self.state()
        self.assertEqual(value["tag"], "v0.1.1")
        self.assertFalse(value["exists"])
        self.assertEqual((self.repo / "VERSION").read_text(), "0.1.1\n")
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual(self.git("tag", "--list"), "v0.1.0")

    def test_explicit_minor_version_is_preserved(self):
        (self.repo / "VERSION").write_text("0.7.0\n")
        self.commit("minor release")
        self.assertEqual(self.state()["tag"], "v0.7.0")

    def test_reuses_tag_for_the_same_commit(self):
        self.git("tag", "-a", "v0.1.1", "-m", "Release 0.1.1")
        self.assertTrue(self.state()["exists"])

    def test_rejects_same_version_on_another_commit(self):
        self.git("tag", "v0.1.1")
        (self.repo / "code.txt").write_text("changed\n")
        self.commit("code change without version change")
        with self.assertRaisesRegex(ValueError, "VERSION.*already"):
            self.state()

    def test_rejects_nonincreasing_version_and_dirty_tree(self):
        self.git("tag", "v0.2.0", "HEAD~1")
        with self.assertRaisesRegex(ValueError, "higher"):
            self.state()
        (self.repo / "VERSION").write_text("0.3.0\n")
        with self.assertRaisesRegex(ValueError, "clean"):
            self.state()

    def test_rejects_a_commit_not_merged_into_main(self):
        self.git("switch", "-c", "codex/feature")
        (self.repo / "code.txt").write_text("unmerged\n")
        self.commit("unmerged code")
        with self.assertRaisesRegex(ValueError, "main"):
            self.state()

    def test_old_tag_retry_does_not_allocate_a_new_version(self):
        self.git("tag", "v0.1.1")
        commit = self.git("rev-parse", "HEAD")
        (self.repo / "VERSION").write_text("0.2.0\n")
        self.commit("next version")
        self.git("tag", "v0.2.0")
        self.git("checkout", "--detach", commit)
        self.assertEqual(self.state()["tag"], "v0.1.1")
        self.assertTrue(self.state()["exists"])


if __name__ == "__main__":
    unittest.main()
