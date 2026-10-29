# Releasing

Use a `codex/` feature branch. Update `VERSION` and `CHANGELOG.md`, run `make verify`, regenerate schemas after public type changes, and inspect the public tree for credentials or private artifacts. Never commit plan files.

GoReleaser 2.19.0 builds Darwin/Linux arm64/amd64 with CGO disabled. Version, full commit and date are injected. Releases include binary archives, a source archive and `checksums.txt`.

```sh
goreleaser check
goreleaser release --snapshot --clean
```

Move the tested feature commit into remote `dev` through the repository's review process. The release helper requires a clean tree and `HEAD` in `origin/dev`. It never stages files, creates commits or changes branches:

```sh
python3 scripts/release.py --version 0.1.0
python3 scripts/release.py --version 0.1.0 --publish
```

The first is a dry run. The second creates and pushes the exact annotated tag. Actions verifies `VERSION`, dev ancestry and the full suite, then publishes using its repository token. AI credentials are not required.

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

The `Homebrew` workflow runs after the tag release job succeeds. It also handles stable releases published outside that workflow and supports manual dispatch with a stable tag. The direct workflow call is required because a release created with `GITHUB_TOKEN` does not trigger another workflow through the release event.

The workflow uses three separate jobs. Preparation verifies release checksums and updates a dedicated formula branch. Validation installs and tests its exact commit on macOS, then runs Homebrew style and strict online audit. Publication starts on a fresh runner with the same publisher source commit used in preparation and publishes only the validated formula SHA. Runs are serialized. A failed package check leaves the prepared branch available for inspection and the published tap unchanged. Re-run the workflow after fixing a failure; a concurrent tap change requires fresh preparation and validation.

Set `HOMEBREW_TAP_TOKEN` in the marina-the-reviewer repository Actions secrets. Use a fine-grained token limited to `maximgorbatyuk/homebrew-tap` with Contents read/write. Automatic publication uses `--no-pr` and does not require Pull requests permission. The optional manual PR mode also needs Pull requests read/write. The token is passed only to preparation and publication; the package validation job receives no tap write token and does not share a runner with publication. Do not copy the local developer OAuth token into Actions. A missing, expired or insufficient token fails the workflow visibly. Renew the secret before its expiry.

```sh
gh workflow run homebrew.yml --repo maximgorbatyuk/marina-the-reviewer --ref dev -f tag=v0.1.0
```

Manual dispatch rechecks a previously published release without creating a new tag or running paid AI requests. An unchanged formula skips installation and publication. If the release version is older than the tap version, the workflow leaves the newer version in place.
