# Agent instructions

- Start responses to Maxim with `Maxim`.
- This repository builds a Go CLI named `marinadoreview`. The parent directory's Rust conventions do not apply.
- Use `rtk` for local shell commands. Use `rtk proxy` for commands without a dedicated wrapper. Public scripts and workflows do not require RTK.
- Work on `codex/` branches. Do not commit on `main`, `master`, `stage`, or `dev`. Use conventional commit messages in Simplified Technical English, with no agent attribution.
- Before code changes, state the scope in three to five bullets. Use tests to establish behavior before implementation. Run the full Go test suite and build after code changes.
- Keep the CLI independent of Marina. Read source contracts from `/Users/maximgorbatyuk/projects/petrel/petrel-infrastructure/gitlab-ai` only. Do not edit that checkout, its tests, configuration, database, or service.
- Git preparation reads pinned objects. Do not change the reviewed repository, run its hooks, filters or build scripts, or fetch automatically.
- Tests use temporary repositories and local fake HTTP servers. Do not use real credentials, databases, or paid AI requests in the default suite.
- Keep credentials out of plans, results, journals, diagnostics and fixtures. Redact them before logging errors.
- Plans are local work files. Do not commit plans or reference them from source comments or permanent documentation.
- Comments explain a necessary reason, use Simplified Technical English, and stay within two sentences.
- Before release, run `make verify`, script tests, packaging checks and offline smoke tests. Do not report runtime or release success from source inspection alone.

## Publish a new version to Homebrew

Follow these steps for a requested release. Run commands from this tool's checkout. Replace `X.Y.Z`, `RELEASE_PR`, and `RELEASE_RUN_ID` with the selected version and the actual GitHub IDs. Check [RELEASING.md](RELEASING.md), `.github/workflows/release.yml`, `.github/workflows/homebrew.yml`, and the release scripts if this process changes.

### 1. Check tools, access, and release refs

Use the Go version required by `go.mod`, Python 3, Git, GitHub CLI (`gh`), GoReleaser 2.19.0, and Homebrew on macOS for package validation. Fetch only this tool repository; do not fetch repositories submitted for review.

```sh
rtk git status --short --branch
rtk git remote -v
rtk git fetch origin --tags
rtk git tag --list 'v*' --sort=-version:refname
rtk proxy gh auth status --hostname github.com
rtk proxy gh secret list --repo maximgorbatyuk/marina-the-reviewer
```

The source repository's Actions secrets must include `HOMEBREW_TAP_TOKEN`. Use a fine-grained token restricted to `maximgorbatyuk/homebrew-tap` with Contents read/write. Automatic publication uses `--no-pr`; it does not need Pull requests access. Manual PR mode also needs Pull requests read/write. Store the token as an Actions secret; keep its value out of commands, files, and logs. Listing the secret confirms its name, but the Homebrew workflow must confirm that its permissions and expiry are valid.

### 2. Prepare the version and run local checks

Work on a `codex/` branch, or on `dev` when Maxim explicitly requests it. Preserve unrelated changes. Set `VERSION` to a stable `X.Y.Z` without a `v` prefix, higher than every existing stable tag, and update `CHANGELOG.md`. Actions reads this value without incrementing it. If public types changed, regenerate and inspect the schemas:

```sh
rtk proxy go run scripts/generate_schemas.go
```

Run the release checks:

```sh
rtk proxy make verify
rtk proxy goreleaser --version
rtk proxy goreleaser check
rtk proxy goreleaser release --snapshot --clean
```

`make verify` checks formatting, vet, the full Go test suite, race tests, builds, and all Python script tests. Check that the snapshot contains Darwin/Linux arm64/amd64 archives and `checksums.txt`. Select the native binary under `dist/` from the GoReleaser output and run these offline smoke commands, replacing the example path:

```sh
release_snapshot_bin=/absolute/path/to/native/snapshot/marinadoreview
rtk proxy "$release_snapshot_bin" --version --json
rtk proxy "$release_snapshot_bin" --help
rtk proxy "$release_snapshot_bin" capabilities --json
```

A snapshot reports a development version. `make build` also adds `-dev`; neither proves the published release identity. Do not run paid AI requests for release checks. Keep `dist/`, credentials, review artifacts, and local plans out of the commit.

### 3. Commit the release and merge it into main

Stage the intended release files by explicit path, including `VERSION`, `CHANGELOG.md`, and any changed code or schemas. Review the staged diff and commit on the development branch:

```sh
rtk git add -- VERSION CHANGELOG.md
rtk git diff --cached
rtk git commit -m 'chore: release X.Y.Z'
rtk git status --short
rtk git push origin HEAD
```

Require a clean tree and use a reviewed pull request targeting `main`. If no release PR exists, create it from the pushed development branch, then use the returned PR number for the checks and merge:

```sh
rtk proxy gh pr create --repo maximgorbatyuk/marina-the-reviewer --base main --fill
rtk proxy gh pr checks RELEASE_PR --repo maximgorbatyuk/marina-the-reviewer --watch
rtk proxy gh pr merge RELEASE_PR --repo maximgorbatyuk/marina-the-reviewer --merge
```

A push to `dev` or `codex/**` runs CI only. The resulting push to `main` starts `Release`, which runs Linux/macOS CI, creates or reuses the annotated `vX.Y.Z` tag for the release commit, publishes assets with GoReleaser, and directly calls the Homebrew workflow. Keep this direct call: tags and releases created with `GITHUB_TOKEN` do not start another workflow through their push/release events.

### 4. Verify the release and Homebrew publication

```sh
rtk proxy gh run list --repo maximgorbatyuk/marina-the-reviewer --workflow release.yml --branch main --limit 5
rtk proxy gh run watch RELEASE_RUN_ID --repo maximgorbatyuk/marina-the-reviewer --exit-status
rtk proxy gh run view RELEASE_RUN_ID --repo maximgorbatyuk/marina-the-reviewer
rtk proxy gh release view vX.Y.Z --repo maximgorbatyuk/marina-the-reviewer --json tagName,isDraft,isPrerelease,targetCommitish,assets,url
rtk proxy gh api repos/maximgorbatyuk/marina-the-reviewer/commits/vX.Y.Z --jq .sha
rtk proxy python3 scripts/update_homebrew.py --tag vX.Y.Z
rtk proxy gh api repos/maximgorbatyuk/homebrew-tap/contents/Formula/marina-the-reviewer.rb --jq '.content | @base64d'
```

Select the run for the intended release commit. Require a published stable release with `checksums.txt`, `marina-the-reviewer_X.Y.Z_source.tar.gz`, and four nonempty binary archives named `marina-the-reviewer_X.Y.Z_{darwin,linux}_{arm64,amd64}.tar.gz`. Resolve the tag to its commit and retain that SHA for the installed binary check. The Python command downloads the source archive, verifies its SHA-256 against `checksums.txt`, and prints the expected formula without changing the tap. Compare it with the formula read back from the tap's default branch by the final command.

Check all three Homebrew jobs in the Release run. `prepare` verifies the source and updates `codex/marina-the-reviewer-vX.Y.Z` in `maximgorbatyuk/homebrew-tap`. `validate` checks out the exact prepared SHA on macOS, runs `brew install --build-from-source`, `brew test`, `brew style`, and `brew audit --strict --online`. The formula's offline test covers version, help, usage, capabilities, and `prepare` / `plan` / `validate` in a temporary Git repository. `publish` then fast-forwards the tap default branch to that validated SHA, updating `Formula/marina-the-reviewer.rb` without force. A failed validation must be fixed before publication.

### 5. Verify installation from the published tap

Run on a macOS validation machine:

```sh
rtk proxy brew update
rtk proxy brew install maximgorbatyuk/tap/marina-the-reviewer
rtk proxy brew test maximgorbatyuk/tap/marina-the-reviewer
rtk proxy brew list --versions maximgorbatyuk/tap/marina-the-reviewer
release_brew_bin="$(rtk proxy brew --prefix maximgorbatyuk/tap/marina-the-reviewer)/bin/marinadoreview"
rtk proxy "$release_brew_bin" --version --json
rtk proxy "$release_brew_bin" capabilities --json
rtk proxy which -a marinadoreview
rtk proxy marinadoreview --version --json
```

If the formula is already installed, use `rtk proxy brew upgrade maximgorbatyuk/tap/marina-the-reviewer` in place of `brew install`. Require JSON `version` equal to `X.Y.Z` and `commit` equal to the release tag's resolved commit. Check that the command on `PATH` resolves to the same release. Report the tag, commit, Actions result, tap publication, and installed version separately; a GitHub release alone does not prove a Homebrew update.

### Recovery

- For a failed Release run, inspect `rtk proxy gh run view RELEASE_RUN_ID --repo maximgorbatyuk/marina-the-reviewer --log-failed`, fix the cause, and rerun with `rtk proxy gh run rerun RELEASE_RUN_ID --repo maximgorbatyuk/marina-the-reviewer --failed`. A complete release is reused; an incomplete existing release requires inspection of its assets. Never overwrite a published tag or release to add new code. New code needs a higher `VERSION`.
- If the release is complete and only Homebrew needs retrying, dispatch its workflow and watch the resulting run:

  ```sh
  rtk proxy gh workflow run homebrew.yml --repo maximgorbatyuk/marina-the-reviewer --ref main -f tag=vX.Y.Z
  rtk proxy gh run list --repo maximgorbatyuk/marina-the-reviewer --workflow homebrew.yml --limit 5
  ```

  Use `gh run watch` and `gh run view` as above with that run's ID, then repeat the installation checks. An unchanged formula skips validation and publication; an older release cannot replace a newer tap version. Confirm the installed version even after a successful no-op run.
- For explicit tag recovery, use a clean checkout at the intended release commit already merged into `origin/main`. `rtk proxy python3 scripts/release.py` runs local checks and prints a dry run; `rtk proxy python3 scripts/release.py --publish` creates and pushes the matching tag. It does not stage files, create commits, change branches, or edit `VERSION`.
- Prefer the Homebrew workflow for recovery. In manual script mode, `update_homebrew.py --publish` only prepares a branch and optional PR. `--merge --head-sha VERIFIED_HEAD` publishes it. Validate that exact formula SHA with all Homebrew package checks before using `--merge`; repeat preparation and validation if the formula head or tap base changes. See `RELEASING.md` for the manual path.
