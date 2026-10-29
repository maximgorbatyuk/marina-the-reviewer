import unittest

from update_homebrew import checksum_for, release_version


class HomebrewUpdateTests(unittest.TestCase):
    def test_only_stable_release_tags_are_accepted(self):
        self.assertEqual(release_version("v1.2.3"), "1.2.3")
        for value in ("1.2.3", "v1.2.3-rc1", "v01.2.3", "v1.2.3\n"):
            with self.assertRaises(ValueError):
                release_version(value)

    def test_checksum_requires_one_exact_asset(self):
        digest = "a" * 64
        self.assertEqual(checksum_for(f"{digest}  source.tar.gz\n", "source.tar.gz"), digest)
        for text in (f"{digest}  other.tar.gz\n", f"{digest}  ../source.tar.gz\n", f"{digest}  source.tar.gz\n" * 2):
            with self.assertRaises(ValueError):
                checksum_for(text, "source.tar.gz")
