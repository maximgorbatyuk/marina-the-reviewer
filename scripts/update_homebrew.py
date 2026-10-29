#!/usr/bin/env python3
"""Verify a release and prepare its Homebrew tap pull request."""

import argparse
import base64
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile
from urllib.parse import quote

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
    try:
        result = subprocess.run(command, input=data, text=True, capture_output=True, check=True)
    except subprocess.CalledProcessError as error:
        details = (error.stderr or "GitHub command failed").strip()
        for name in ("GH_TOKEN", "GITHUB_TOKEN"):
            secret = os.environ.get(name)
            if secret:
                details = details.replace(secret, "[REDACTED]")
        raise RuntimeError(details) from None
    return result.stdout.strip()


def api(path: str, payload=None, method=None):
    options = ("--method", method) if method else ()
    output = gh("api", path, *options, payload=payload)
    return json.loads(output) if output else None


def formula_at(ref):
    suffix = "?ref=" + quote(ref, safe="")
    files = api(f"repos/{TAP}/contents/Formula{suffix}")
    metadata = next((item for item in files if item["path"] == FORMULA), None)
    if metadata is None:
        return None, None
    content = api(f"repos/{TAP}/contents/{FORMULA}{suffix}")
    return base64.b64decode(content["content"]).decode(), metadata["sha"]


def skip_reason(current, version, formula):
    if current is None:
        return None
    prefix = re.escape(f'https://github.com/{REPOSITORY}/releases/download/v')
    match = re.search(prefix + r'([0-9]+\.[0-9]+\.[0-9]+)/marina-the-reviewer_([0-9]+\.[0-9]+\.[0-9]+)_source\.tar\.gz"', current)
    if not match or match[1] != match[2]:
        raise ValueError("Current formula has an unsupported release URL")
    existing = release_version("v" + match[1])
    if tuple(map(int, existing.split("."))) > tuple(map(int, version.split("."))):
        return "newer_version"
    return "unchanged" if current == formula else None


def branch_name(version):
    release_version("v" + version)
    return f"codex/marina-the-reviewer-v{version}"


def checked_pr(version, base):
    branch = branch_name(version)
    prs = json.loads(gh("pr", "list", "--repo", TAP, "--head", branch, "--base", base, "--state", "open", "--json", "number,url"))
    if len(prs) != 1:
        raise ValueError("Expected exactly one open formula pull request")
    pr = api(f"repos/{TAP}/pulls/{prs[0]['number']}")
    if pr["state"] != "open" or pr.get("draft") or pr["head"]["ref"] != branch or pr["base"]["ref"] != base or pr["head"]["repo"]["full_name"] != TAP or pr["base"]["repo"]["full_name"] != TAP:
        raise ValueError("Formula pull request identity changed")
    return pr


def check_changes(base_sha, head_sha, formula):
    if not re.fullmatch(r"[0-9a-f]{40}", head_sha):
        raise ValueError("Invalid formula head SHA")
    comparison = api(f"repos/{TAP}/compare/{base_sha}...{head_sha}")
    if comparison["status"] != "ahead":
        raise ValueError("Tap base changed; prepare and validate the formula again")
    files = comparison["files"]
    if len(files) != 1 or files[0]["filename"] != FORMULA or files[0]["status"] not in ("added", "modified") or files[0].get("previous_filename"):
        raise ValueError("Formula pull request contains unrelated files")
    actual, _ = formula_at(head_sha)
    if actual != formula:
        raise ValueError("Prepared formula does not match the verified release")


def prepare_update(version, formula):
    base = api(f"repos/{TAP}")["default_branch"]
    base_sha = api(f"repos/{TAP}/git/ref/heads/{base}")["object"]["sha"]
    current, _ = formula_at(base_sha)
    reason = skip_reason(current, version, formula)
    if reason:
        return {"changed": False, "reason": reason}
    branch = branch_name(version)
    refs = api(f"repos/{TAP}/git/matching-refs/heads/{branch}")
    existing = next((ref for ref in refs if ref["ref"] == f"refs/heads/{branch}"), None)
    if existing is None:
        api(f"repos/{TAP}/git/refs", {"ref": f"refs/heads/{branch}", "sha": base_sha})
    else:
        comparison = api(f"repos/{TAP}/compare/{base_sha}...{existing['object']['sha']}")
        if comparison["status"] in ("behind", "diverged"):
            api(f"repos/{TAP}/merges", {"base": branch, "head": base_sha, "commit_message": "chore: sync Homebrew tap"})
    content, blob = formula_at(branch)
    if content != formula:
        payload = {"message": f"chore: update marinadoreview to {version}", "content": base64.b64encode(formula.encode()).decode(), "branch": branch}
        if blob:
            payload["sha"] = blob
        api(f"repos/{TAP}/contents/{FORMULA}", payload, method="PUT")
    prs = json.loads(gh("pr", "list", "--repo", TAP, "--head", branch, "--base", base, "--state", "open", "--json", "number,url"))
    if not prs:
        with tempfile.NamedTemporaryFile(mode="w", suffix=".md") as body:
            body.write(f"Update `marinadoreview` to {version} using the verified source archive.\n\nGitHub Actions installs, tests and audits this exact formula commit before publication.\n")
            body.flush()
            gh("pr", "create", "--repo", TAP, "--head", branch, "--base", base, "--title", f"chore: update marinadoreview to {version}", "--body-file", body.name)
    pr = checked_pr(version, base)
    head = pr["head"]["sha"]
    check_changes(base_sha, head, formula)
    return {"changed": True, "branch": branch, "head_sha": head, "pr_url": pr["html_url"]}


def publish_update(version, formula, verified_head):
    if not re.fullmatch(r"[0-9a-f]{40}", verified_head):
        raise ValueError("Invalid verified head SHA")
    base = api(f"repos/{TAP}")["default_branch"]
    base_sha = api(f"repos/{TAP}/git/ref/heads/{base}")["object"]["sha"]
    current, _ = formula_at(base_sha)
    reason = skip_reason(current, version, formula)
    if reason:
        return {"published": False, "reason": reason}
    pr = checked_pr(version, base)
    if pr["head"]["sha"] != verified_head:
        raise ValueError("Formula pull request head changed after validation")
    check_changes(base_sha, verified_head, formula)
    api(f"repos/{TAP}/git/refs/heads/{base}", {"sha": verified_head, "force": False}, method="PATCH")
    return {"published": True, "head_sha": verified_head, "pr_url": pr["html_url"]}


def write_outputs(values, path):
    lines = []
    for key, value in values.items():
        value = str(value).lower() if isinstance(value, bool) else str(value)
        if not re.fullmatch(r"[a-z_]+", key) or "\n" in value or "\r" in value:
            raise ValueError("Invalid GitHub Actions output")
        lines.append(f"{key}={value}\n")
    with open(path, "a") as output:
        output.writelines(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--publish", action="store_true", help="Prepare a formula pull request")
    mode.add_argument("--merge", action="store_true", help="Publish the exact validated pull request head")
    parser.add_argument("--head-sha")
    args = parser.parse_args()
    if bool(args.head_sha) != args.merge:
        parser.error("--merge requires --head-sha; other modes do not accept it")
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
    if not args.publish and not args.merge:
        print(formula, end="")
        return
    result = publish_update(version, formula, args.head_sha) if args.merge else prepare_update(version, formula)
    if os.environ.get("GITHUB_OUTPUT"):
        write_outputs(result, os.environ["GITHUB_OUTPUT"])
    print(json.dumps(result))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError) as error:
        print(f"Homebrew update failed: {error}", file=sys.stderr)
        sys.exit(1)
