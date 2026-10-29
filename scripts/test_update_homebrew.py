import base64
import json
import tempfile
import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.parse import parse_qs, urlparse

import update_homebrew as update
from render_homebrew import render
from update_homebrew import checksum_for, release_version

BASE = "a" * 40
HEAD = "b" * 40
COMMIT = "c" * 40
BRANCH = "codex/marina-the-reviewer-v0.1.0"


class TapAPI:
    def __init__(self, version="0.0.9"):
        self.current = render(version, COMMIT, "d" * 64)
        self.desired = render("0.1.0", COMMIT, "e" * 64)
        self.branch_exists = False
        self.branch_formula = self.current
        self.pr_exists = False
        self.pr_head = HEAD
        self.status = "ahead"
        self.paths = [update.FORMULA]
        self.writes = []

    def api(self, path, payload=None, method=None):
        if payload is not None:
            self.writes.append((path, payload, method))
        prefix = f"repos/{update.TAP}"
        if path == prefix:
            return {"default_branch": "main"}
        if path == prefix + "/git/ref/heads/main":
            return {"object": {"sha": BASE}}
        if "/git/matching-refs/" in path:
            return [{"ref": "refs/heads/" + BRANCH, "object": {"sha": HEAD}}] if self.branch_exists else []
        if path == prefix + "/git/refs":
            self.branch_exists = True
            return {"object": {"sha": HEAD}}
        if path == prefix + "/merges":
            return {"sha": HEAD}
        if "/contents/Formula?" in path:
            return [{"path": update.FORMULA, "sha": "f" * 40}]
        if path.startswith(prefix + "/contents/" + update.FORMULA):
            if payload is not None:
                self.branch_formula = base64.b64decode(payload["content"]).decode()
                return {"commit": {"sha": HEAD}}
            ref = parse_qs(urlparse(path).query)["ref"][0]
            content = self.current if ref == BASE else self.branch_formula
            return {"content": base64.b64encode(content.encode()).decode(), "sha": "f" * 40}
        if "/compare/" in path:
            return {"status": self.status, "files": [{"filename": p, "status": "modified"} for p in self.paths]}
        if path == prefix + "/pulls/2":
            return {"state": "open", "draft": False, "html_url": f"https://github.com/{update.TAP}/pull/2",
                    "head": {"sha": self.pr_head, "ref": BRANCH, "repo": {"full_name": update.TAP}},
                    "base": {"ref": "main", "repo": {"full_name": update.TAP}}}
        if path == prefix + "/git/refs/heads/main":
            self.current = self.desired
            return {"object": {"sha": payload["sha"]}}
        raise AssertionError(f"Unexpected API request: {path}")

    def gh(self, *args, **kwargs):
        if args[:2] == ("pr", "list"):
            return json.dumps([{"number": 2, "url": f"https://github.com/{update.TAP}/pull/2"}] if self.pr_exists else [])
        if args[:2] == ("pr", "create"):
            self.pr_exists = True
            return f"https://github.com/{update.TAP}/pull/2"
        raise AssertionError(f"Unexpected gh command: {args}")


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

    def test_repeated_release_does_not_create_an_empty_pr(self):
        service = TapAPI("0.1.0")
        service.current = service.desired
        with patch.object(update, "api", side_effect=service.api), patch.object(update, "gh", side_effect=service.gh):
            result = update.prepare_update("0.1.0", service.desired)
        self.assertFalse(result["changed"])
        self.assertEqual(service.writes, [])

    def test_old_release_cannot_downgrade_the_tap(self):
        service = TapAPI("0.2.0")
        with patch.object(update, "api", side_effect=service.api), patch.object(update, "gh", side_effect=service.gh):
            result = update.prepare_update("0.1.0", service.desired)
        self.assertFalse(result["changed"])
        self.assertEqual(result["reason"], "newer_version")
        self.assertEqual(service.writes, [])

    def test_prepare_creates_only_formula_changes_and_records_exact_head(self):
        service = TapAPI()
        with patch.object(update, "api", side_effect=service.api), patch.object(update, "gh", side_effect=service.gh):
            result = update.prepare_update("0.1.0", service.desired)
        self.assertTrue(result["changed"])
        self.assertEqual(result["head_sha"], HEAD)
        self.assertEqual(result["branch"], BRANCH)
        self.assertFalse(any(path.endswith("/heads/main") for path, _, _ in service.writes))
        self.assertEqual(service.branch_formula, service.desired)

    def test_publish_moves_only_the_verified_head_without_force(self):
        service = TapAPI()
        service.pr_exists = True
        service.branch_formula = service.desired
        with patch.object(update, "api", side_effect=service.api), patch.object(update, "gh", side_effect=service.gh):
            result = update.publish_update("0.1.0", service.desired, HEAD)
        self.assertTrue(result["published"])
        self.assertEqual(service.writes, [(f"repos/{update.TAP}/git/refs/heads/main", {"sha": HEAD, "force": False}, "PATCH")])

    def test_publish_rejects_unverified_pr_head(self):
        service = TapAPI()
        service.pr_exists = True
        service.pr_head = "9" * 40
        with patch.object(update, "api", side_effect=service.api), patch.object(update, "gh", side_effect=service.gh):
            with self.assertRaisesRegex(ValueError, "head"):
                update.publish_update("0.1.0", service.desired, HEAD)
        self.assertEqual(service.writes, [])

    def test_publish_rejects_other_files_and_concurrent_base_changes(self):
        for paths, status in (([update.FORMULA, "README.md"], "ahead"), ([update.FORMULA], "diverged")):
            with self.subTest(paths=paths, status=status):
                service = TapAPI()
                service.pr_exists = True
                service.branch_formula = service.desired
                service.paths, service.status = paths, status
                with patch.object(update, "api", side_effect=service.api), patch.object(update, "gh", side_effect=service.gh):
                    with self.assertRaises(ValueError):
                        update.publish_update("0.1.0", service.desired, HEAD)
                self.assertEqual(service.writes, [])

    def test_publish_rejects_formula_that_does_not_match_release(self):
        service = TapAPI()
        service.pr_exists = True
        with patch.object(update, "api", side_effect=service.api), patch.object(update, "gh", side_effect=service.gh):
            with self.assertRaisesRegex(ValueError, "formula"):
                update.publish_update("0.1.0", service.desired, HEAD)
        self.assertEqual(service.writes, [])

    def test_actions_outputs_cannot_inject_additional_lines(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "outputs"
            with self.assertRaises(ValueError):
                update.write_outputs({"branch": "safe\nchanged=true"}, str(path))
            self.assertFalse(path.exists())

    def test_github_error_keeps_permission_detail_and_redacts_token(self):
        failure = subprocess.CalledProcessError(1, ["gh"], stderr="Resource not accessible by personal access token: private-test-token")
        with patch.dict(update.os.environ, {"GH_TOKEN": "private-test-token"}), patch.object(update.subprocess, "run", side_effect=failure):
            with self.assertRaisesRegex(RuntimeError, "Resource not accessible") as caught:
                update.gh("pr", "create")
        self.assertNotIn("private-test-token", str(caught.exception))
