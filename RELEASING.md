# Releasing

Develop in `dev` or a feature branch. Set the next stable version in `VERSION` and update `CHANGELOG.md` before pushing the release commit to `main`. The version is chosen by the developer; Actions never increments it. Run `make verify`, regenerate schemas after public type changes, and inspect the public tree for credentials or private artifacts. Never commit plan files.

A push to `dev` or a feature branch runs CI only. A push to `main` runs the Release workflow:

1. Run the full CI matrix on Linux and macOS.
2. Read `VERSION` from the exact pushed commit and check that it is merged into `main`.
3. Create and push the annotated tag `vX.Y.Z` for that commit.
4. Build and publish the release archives, source archive and checksums.
5. Validate and publish the Homebrew formula through its isolated workflow.

The source branch of the push does not affect this flow. For example, `VERSION` set to `0.1.1` in `dev` produces tag `v0.1.1` when that commit reaches `main`. No version-bump commit is generated on `main`. Release runs are queued rather than canceled when another push arrives.

An existing tag is reused only when it points to the same commit and matches `VERSION`. New code with an already tagged version fails before publication and requires a version change. A new version must be higher than all existing stable tags. Retrying the original run reuses its tag; a complete published release is not rebuilt or overwritten, and the Homebrew step can continue. An incomplete existing release fails with a diagnostic so its assets can be inspected before retrying.

GoReleaser 2.19.0 builds Darwin/Linux arm64/amd64 with CGO disabled. Version, full commit and date are injected. Releases include binary archives, a source archive and `checksums.txt`.

```sh
goreleaser check
goreleaser release --snapshot --clean
```

Normal releases need only the push to `main`. For a manual preflight or explicit tag recovery, the release helper requires a clean tree and a commit already merged into `origin/main`. It never stages files, creates commits, changes branches or edits `VERSION`:

```sh
python3 scripts/release.py
python3 scripts/release.py --publish
```

The first is a dry run. The second creates and pushes the exact annotated tag, or pushes the matching existing tag. Explicit tag pushes also enter the Release workflow and use the same checks. The optional `--version X.Y.Z` must match the committed `VERSION` file. AI credentials are not required. `make build` reports the file version with a `-dev` suffix; published builds use the exact release version.

Homebrew uses `Formula/marina-the-reviewer.rb` in `maximgorbatyuk/homebrew-tap`. The formula verifies the source archive SHA-256, builds with release identity, and runs offline Git/CLI tests.

```sh
python3 scripts/update_homebrew.py --tag v0.1.0
python3 scripts/update_homebrew.py --tag v0.1.0 --publish
```

The first verifies the downloaded archive against `checksums.txt` and prints the formula. The second creates or updates a dedicated tap branch and PR, preserving unrelated files. It reports the exact head SHA that must be validated before publication. Repeating a published formula is a no-op; an older release cannot replace a newer version. To publish manually after package validation:

```sh
python3 scripts/update_homebrew.py --tag v0.1.0 --merge --head-sha VERIFIED_PR_HEAD
```

The publisher rejects a changed head, a changed tap base, unrelated files, or a formula that differs from the verified release. It fast-forwards the tap default branch without a force update or a new commit on that branch. GitHub marks an associated PR as merged. Add `--no-pr` to both prepare and publication commands to operate through the dedicated branch without using the pull request API. After publication, verify:

```sh
brew update
brew install maximgorbatyuk/tap/marina-the-reviewer
brew test maximgorbatyuk/tap/marina-the-reviewer
marinadoreview --version --json
```

## Automatic publication

The `Homebrew` workflow runs after the Release job succeeds. It also handles stable releases published outside that workflow and supports manual dispatch with a stable tag. The direct workflow calls are required because tags and releases created with `GITHUB_TOKEN` do not start another workflow through push/release events.

The workflow uses three separate jobs. Preparation verifies release checksums and updates a dedicated formula branch. Validation installs and tests its exact commit on macOS, then runs Homebrew style and strict online audit. Publication starts on a fresh runner with the same publisher source commit used in preparation and publishes only the validated formula SHA. Runs are serialized. A failed package check leaves the prepared branch available for inspection and the published tap unchanged. Re-run the workflow after fixing a failure; a concurrent tap change requires fresh preparation and validation.

Set `HOMEBREW_TAP_TOKEN` in the marina-the-reviewer repository Actions secrets. Use a fine-grained token limited to `maximgorbatyuk/homebrew-tap` with Contents read/write. Automatic publication uses `--no-pr` and does not require Pull requests permission. The optional manual PR mode also needs Pull requests read/write. The token is passed only to preparation and publication; the package validation job receives no tap write token and does not share a runner with publication. Do not copy the local developer OAuth token into Actions. A missing, expired or insufficient token fails the workflow visibly. Renew the secret before its expiry.

```sh
gh workflow run homebrew.yml --repo maximgorbatyuk/marina-the-reviewer --ref main -f tag=v0.1.1
```

Manual dispatch rechecks a previously published release without creating a new tag or running paid AI requests. An unchanged formula skips installation and publication. If the release version is older than the tap version, the workflow leaves the newer version in place.
