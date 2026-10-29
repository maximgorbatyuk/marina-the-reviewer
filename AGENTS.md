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
