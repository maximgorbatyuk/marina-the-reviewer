#!/usr/bin/env python3
"""Verify a release and prepare its Homebrew tap pull request."""

import argparse
import base64
import hashlib
import json
import pathlib
import re
import subprocess
import tempfile

from render_homebrew import render

REPOSITORY = "maximgorbatyuk/marina-the-reviewer"
TAP = "maximgorbatyuk/homebrew-tap"
FORMULA = "Formula/marina-the-reviewer.rb"


def release_version(tag: str) -> str:
    if not re.fullmatch(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", tag):
        raise ValueError("Expected a stable release tag vX.Y.Z")
    return tag[1:]


def checksum_for(text: str, name: str) -> str:
    values = []
    for line in text.splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})\s+\*?([^\s]+)", line)
        if match and match[2] == name:
            values.append(match[1])
    if len(values) != 1:
        raise ValueError("Release checksum must name exactly one source archive")
    return values[0]


def gh(*args: str, payload=None):
    command = ["gh", *args]
    data = None
    if payload is not None:
        command += ["--input", "-"]
        data = json.dumps(payload)
    result = subprocess.run(command, input=data, text=True, capture_output=True, check=True)
    return result.stdout.strip()


def api(path: str, payload=None, method=None):
    options = ("--method", method) if method else ()
    return json.loads(gh("api", path, *options, payload=payload))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--publish", action="store_true")
    args = parser.parse_args()
    version = release_version(args.tag)
    release = api(f"repos/{REPOSITORY}/releases/tags/{args.tag}")
    if release["draft"] or release["prerelease"] or release["tag_name"] != args.tag:
        raise ValueError("Only a published stable release can update the tap")
    commit = api(f"repos/{REPOSITORY}/commits/{args.tag}")["sha"]
    name = f"marina-the-reviewer_{version}_source.tar.gz"
    with tempfile.TemporaryDirectory(prefix="marina-homebrew-") as directory:
        gh("release", "download", args.tag, "--repo", REPOSITORY, "--pattern", "checksums.txt", "--pattern", name, "--dir", directory)
        root = pathlib.Path(directory)
        digest = checksum_for((root / "checksums.txt").read_text(), name)
        if hashlib.sha256((root / name).read_bytes()).hexdigest() != digest:
            raise ValueError("Downloaded source archive does not match its release checksum")
    formula = render(version, commit, digest)
    if not args.publish:
        print(formula, end="")
        return
    base = api(f"repos/{TAP}")["default_branch"]
    branch = f"codex/marina-the-reviewer-{args.tag}"
    refs = api(f"repos/{TAP}/git/matching-refs/heads/{branch}")
    if not any(ref["ref"] == f"refs/heads/{branch}" for ref in refs):
        sha = api(f"repos/{TAP}/git/ref/heads/{base}")["object"]["sha"]
        api(f"repos/{TAP}/git/refs", {"ref": f"refs/heads/{branch}", "sha": sha})
    files = api(f"repos/{TAP}/contents/Formula?ref={branch}")
    existing = next((item for item in files if item["path"] == FORMULA), None)
    payload = {"message": f"feat: add marina reviewer {version}", "content": base64.b64encode(formula.encode()).decode(), "branch": branch}
    if existing:
        current = api(f"repos/{TAP}/contents/{FORMULA}?ref={branch}")
        if base64.b64decode(current["content"]).decode() == formula:
            payload = None
        else:
            payload["sha"] = existing["sha"]
    if payload:
        api(f"repos/{TAP}/contents/{FORMULA}", payload, method="PUT")
    prs = json.loads(gh("pr", "list", "--repo", TAP, "--head", branch, "--state", "open", "--json", "url"))
    if prs:
        print(prs[0]["url"])
        return
    with tempfile.NamedTemporaryFile(mode="w", suffix=".md") as body:
        body.write(f"Add `marinadoreview` {version} from the published source archive.\n\nThe source SHA-256 matches `checksums.txt` in [{args.tag}](https://github.com/{REPOSITORY}/releases/tag/{args.tag}). The formula builds from source and runs offline Git preparation tests.\n")
        body.flush()
        print(gh("pr", "create", "--repo", TAP, "--head", branch, "--base", base, "--title", f"feat: add marina reviewer {version}", "--body-file", body.name))


if __name__ == "__main__":
    main()
