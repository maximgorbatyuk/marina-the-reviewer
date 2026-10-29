import importlib.util
import pathlib
import tempfile
import unittest


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


if __name__ == "__main__":
    unittest.main()
