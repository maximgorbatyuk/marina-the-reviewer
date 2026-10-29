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

The first verifies the downloaded archive against `checksums.txt` and prints the formula. The second creates or updates a dedicated tap branch and PR, preserving unrelated files. Review and merge the PR, then verify:

```sh
brew update
brew install maximgorbatyuk/tap/marina-the-reviewer
brew test maximgorbatyuk/tap/marina-the-reviewer
marinadoreview --version --json
```

Automatic tap PRs require `HOMEBREW_TAP_TOKEN`, a fine-grained Actions secret with Contents write and Pull requests write access to the tap. Do not copy the local developer OAuth token into Actions. Without the secret, binary publication succeeds and the workflow reports the manual tap step. After configuring it, dispatch Homebrew for a published stable tag. Repeated runs reuse the same branch and open PR.
