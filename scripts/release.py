#!/usr/bin/env python3
"""Check a release before creating its tag."""

import argparse
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


def run(command: list[str], capture: bool = False) -> str:
    result = subprocess.run(command, cwd=ROOT, check=True, text=True, capture_output=capture)
    return result.stdout.strip() if capture else ""


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version")
    parser.add_argument("--check-tag")
    parser.add_argument("--publish", action="store_true")
    args = parser.parse_args()
    current = validate_version((ROOT / "VERSION").read_text().strip())
    version = validate_version(args.version or current)
    if version != current:
        raise ValueError("Update VERSION in a reviewed feature commit before release")
    if args.check_tag:
        if args.check_tag != f"v{version}":
            raise ValueError("Release tag does not match VERSION")
        return 0
    if run(["git", "status", "--porcelain"], capture=True):
        raise ValueError("Working tree must be clean; no files will be staged automatically")
    run(["make", "verify"])
    run(["goreleaser", "check"])
    run(["git", "merge-base", "--is-ancestor", "HEAD", "origin/dev"])
    actions = release_plan(version)
    for action in actions:
        print(" ".join(action))
        if args.publish:
            run(action)
    if not args.publish:
        print("Dry run complete. No tags or remote changes were made.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, subprocess.CalledProcessError) as error:
        print(f"Release failed: {error}", file=sys.stderr)
        sys.exit(1)
