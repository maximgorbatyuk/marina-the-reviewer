#!/usr/bin/env python3
"""Record synthetic compatibility cases with a pinned, isolated C# source tree."""

import argparse
import hashlib
import io
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

BASELINE = "acd37b498f1c9367b4044e8f9d5ad55b90617e59"
SOURCE_PATHS = (
    "gitlab-ai/src/Petrel.GitLabAi.Application",
    "gitlab-ai/src/Petrel.GitLabAi.Domain",
)


def isolated_source(source: Path, destination: Path, baseline: str) -> Path:
    repository = subprocess.run(
        ["git", "-C", str(source), "rev-parse", "--show-toplevel"],
        check=True, capture_output=True, text=True,
    ).stdout.strip()
    archive = subprocess.run(
        ["git", "-C", repository, "archive", "--format=tar", baseline, *SOURCE_PATHS],
        check=True, capture_output=True,
    ).stdout
    with tarfile.open(fileobj=io.BytesIO(archive)) as stream:
        for member in stream.getmembers():
            target = destination / member.name
            if not target.resolve().is_relative_to(destination.resolve()):
                raise ValueError("Unsafe archive member")
            if member.issym() or member.islnk():
                raise ValueError("Source archive links are not supported")
            if member.isfile():
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(stream.extractfile(member).read())
    return destination / "gitlab-ai"


def generate(source: Path, output: Path, baseline: str = BASELINE) -> dict:
    root = Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix="marina-oracle-") as temp:
        work = Path(temp).resolve()
        copied = isolated_source(source, work / "source", baseline)
        harness = work / "oracle"
        shutil.copytree(root / "tools" / "compat-oracle", harness,
                        ignore=shutil.ignore_patterns("bin", "obj"))
        fixture = work / "stages.json"
        subprocess.run([
            "dotnet", "run", "--project", str(harness / "CompatOracle.csproj"),
            "--property:MarinaSource=" + str(copied),
            "--property:TreatWarningsAsErrors=false", "--", str(fixture),
        ], check=True, cwd=work)
        data = fixture.read_bytes()
        json.loads(data)
        hashes = {}
        for path in sorted(copied.rglob("*.cs")):
            if "obj" not in path.parts and "bin" not in path.parts:
                hashes[str(path.relative_to(copied))] = hashlib.sha256(path.read_bytes()).hexdigest()
        manifest = {
            "baseline": baseline,
            "generator": "tools/compat-oracle/Program.cs",
            "syntheticOnly": True,
            "fixtures": {"stages.json": hashlib.sha256(data).hexdigest()},
            "sourceHashes": hashes,
            "expectedDifferences": [
                "Native Java parsing uses a lexical tokenizer and does not claim Marina AST identity parity.",
                "Native planning preserves every primary line but uses independent range cuts when source alignment is unproven.",
            ],
        }
        output.mkdir(parents=True, exist_ok=True)
        (output / "stages.json").write_bytes(data)
        (output / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
        return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--output", type=Path, default=Path("testdata/compat"))
    parser.add_argument("--baseline", default=BASELINE)
    args = parser.parse_args()
    generate(args.source, args.output, args.baseline)


if __name__ == "__main__":
    main()
