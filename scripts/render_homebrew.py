#!/usr/bin/env python3
"""Render a formula from verified release metadata."""

import argparse
import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parents[1]


def render(version: str, commit: str, digest: str) -> str:
    if not re.fullmatch(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", version):
        raise ValueError("Invalid release version")
    if not re.fullmatch(r"[0-9a-f]{40}", commit) or not re.fullmatch(r"[0-9a-f]{64}", digest):
        raise ValueError("Invalid release commit or SHA-256")
    template = (ROOT / "packaging/homebrew/marina-the-reviewer.rb.tmpl").read_text()
    return template.replace("@VERSION@", version).replace("@COMMIT@", commit).replace("@SHA256@", digest)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--sha256", required=True)
    args = parser.parse_args()
    print(render(args.version, args.commit, args.sha256), end="")
