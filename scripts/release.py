#!/usr/bin/env python3
"""Check a release before creating its tag."""

import argparse
import json
import os
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]


def validate_version(value: str) -> str:
    if not re.fullmatch(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", value):
        raise ValueError("Expected a stable version X.Y.Z")
    return value


def release_plan(version: str) -> list[list[str]]:
    validate_version(version)
    return [["git", "tag", "-a", f"v{version}", "-m", f"Release {version}"],
            ["git", "push", "origin", f"v{version}"]]


def run(command: list[str], capture: bool = False, repo=ROOT) -> str:
    result = subprocess.run(command, cwd=repo, check=True, text=True, capture_output=capture)
    return result.stdout.strip() if capture else ""


def release_state(repo=ROOT, main_ref="origin/main") -> dict:
    repo = pathlib.Path(repo)
    top = run(["git", "rev-parse", "--show-toplevel"], capture=True, repo=repo)
    if pathlib.Path(top).resolve() != repo.resolve():
        raise ValueError("Release commands must use this repository root")
    if run(["git", "status", "--porcelain"], capture=True, repo=repo):
        raise ValueError("Working tree must be clean; no files will be staged automatically")
    version = validate_version((repo / "VERSION").read_text().strip())
    commit = run(["git", "rev-parse", "HEAD"], capture=True, repo=repo)
    ancestor = subprocess.run(["git", "merge-base", "--is-ancestor", commit, main_ref], cwd=repo, capture_output=True)
    if ancestor.returncode != 0:
        raise ValueError("The release commit must be merged into main")
    tag = "v" + version
    existing = subprocess.run(["git", "rev-parse", "--verify", "--quiet", f"refs/tags/{tag}^{{commit}}"], cwd=repo, text=True, capture_output=True)
    if existing.returncode == 0:
        if existing.stdout.strip() != commit:
            raise ValueError(f"VERSION {version} already belongs to another commit; increase VERSION")
        return {"version": version, "tag": tag, "commit": commit, "exists": True}
    versions = []
    for candidate in run(["git", "tag", "--list", "v*"], capture=True, repo=repo).splitlines():
        try:
            versions.append(tuple(map(int, validate_version(candidate[1:]).split("."))))
        except ValueError:
            continue
    if versions and tuple(map(int, version.split("."))) <= max(versions):
        raise ValueError("VERSION must be higher than every existing stable tag")
    return {"version": version, "tag": tag, "commit": commit, "exists": False}


def release_complete(value, version):
    if not value or value.get("tag_name") != "v" + version or value.get("draft") or value.get("prerelease"):
        return False
    expected = {"checksums.txt", f"marina-the-reviewer_{version}_source.tar.gz"}
    expected.update(f"marina-the-reviewer_{version}_{system}_{arch}.tar.gz" for system in ("darwin", "linux") for arch in ("arm64", "amd64"))
    assets = value.get("assets", [])
    return all(sum(item.get("name") == name and item.get("size", 0) > 0 for item in assets) == 1 for name in expected)


def existing_release(tag):
    result = subprocess.run(["gh", "api", f"repos/maximgorbatyuk/marina-the-reviewer/releases/tags/{tag}"], cwd=ROOT, text=True, capture_output=True)
    if result.returncode == 0:
        return json.loads(result.stdout)
    if "(HTTP 404)" in result.stderr:
        return None
    raise ValueError("Cannot read the GitHub release; check the Actions token and GitHub availability")


def ci_context(state):
    ref = os.environ.get("GITHUB_REF", "")
    if os.environ.get("GITHUB_ACTIONS") != "true" or ref not in ("refs/heads/main", "refs/tags/" + state["tag"]):
        raise ValueError("CI publication requires a main push or its exact version tag")
    if os.environ.get("GITHUB_SHA") != state["commit"] or not os.environ.get("GITHUB_OUTPUT"):
        raise ValueError("CI checkout does not match the triggering commit or has no output file")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version")
    parser.add_argument("--check-tag")
    parser.add_argument("--publish", action="store_true")
    parser.add_argument("--ci", action="store_true", help="Use checks completed by the required CI jobs")
    args = parser.parse_args()
    current = validate_version((ROOT / "VERSION").read_text().strip())
    version = validate_version(args.version or current)
    if version != current:
        raise ValueError("Update VERSION in a reviewed feature commit before release")
    if args.ci:
        run(["git", "fetch", "origin", "--tags"])
    state = release_state()
    if args.check_tag:
        if args.check_tag != f"v{version}":
            raise ValueError("Release tag does not match VERSION")
        if not state["exists"]:
            raise ValueError("The release tag must point to this commit")
        return 0
    should_build = True
    if args.ci:
        ci_context(state)
        metadata = existing_release(state["tag"])
        if metadata is not None:
            if not release_complete(metadata, version):
                raise ValueError("The existing release is incomplete; inspect its assets before retrying")
            should_build = False
    else:
        run(["make", "verify"])
        run(["goreleaser", "check"])
    actions = release_plan(version)[1:] if state["exists"] else release_plan(version)
    for action in actions:
        print(" ".join(action))
        if args.publish:
            run(action)
    if args.ci:
        if not args.publish:
            raise ValueError("CI publication requires --publish")
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write(f"tag={state['tag']}\nversion={version}\nbuild={str(should_build).lower()}\n")
    if not args.publish:
        print("Dry run complete. No tags or remote changes were made.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, subprocess.CalledProcessError) as error:
        print(f"Release failed: {error}", file=sys.stderr)
        sys.exit(1)
