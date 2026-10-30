# marina-the-reviewer

`marinadoreview` prepares source code, runs an AI code and architecture review, and validates the answer against the supplied source. It is a standalone Go CLI with an OpenAI-compatible HTTP backend.

It reads pinned Git objects. It does not fetch, change branches, run repository hooks or build scripts, or post comments to a hosting service.

## Install

```sh
brew install maximgorbatyuk/tap/marina-the-reviewer
marinadoreview --version --json
```

Release archives support macOS and Linux on arm64 and amd64. The Homebrew formula builds the release from source. Git is required. To build locally, use Go 1.27.1 or later:

```sh
make build
bin/marinadoreview capabilities --json
```

## Configure providers

Run without arguments in a terminal to open the configuration TUI:

```sh
marinadoreview
marinadoreview --config ./config.local.json
```

The home screen shows the version, configuration path, and primary/fallback status. Open **Providers and routing** to create or edit profiles. Choose an AI provider preset (OpenAI, OpenRouter, DeepSeek, Groq, Ollama, or another OpenAI-compatible API), enter the API URL and authorization source, then load the model list or type a model ID. The model list depends on the server; manual entry remains available when listing is unsupported or unavailable.

Paste an API token into its masked field, select an environment variable or private token file, or explicitly select no authorization for a suitable local server. A blank replacement preserves a saved token. Selecting an authorization source again clears its saved value; changing the API URL requires entering credentials again. Tokens and extra parameter values are hidden in the configuration viewer.

**Test connection** sends one small AI request to the selected model using its JSON settings. It can use billed tokens, sends no source code, and does not retry or switch to fallback. A successful model-list request does not prove inference access. The status shows the last test time and duration; changes reset it, and a new session starts as untested. A small successful test does not guarantee a complete code review or support for every response schema. Offline configuration is supported without a successful test.

Apply profile edits to the draft, then press `s` in Providers to save. Use `p` to assign the selected profile as primary, `f` for fallback, and `d` to disable fallback. Other profiles remain saved. Advanced settings expose JSON mode, token/context limits, timeout, retries, concurrency and streaming; existing extra parameters remain intact. Save validates the whole configuration and writes it atomically with private permissions. If the file changed externally, reload it before saving.

The menu also provides configuration/status details, token and code review statistics in Usage, the full built-in review prompt, and help. Use arrows or `j`/`k` in menus, Tab/Shift+Tab in forms, Enter to select, and Esc to return or cancel a request. Prompt and help viewers support scrolling and Home/End. Terminal paste, including Cmd+V on macOS, is supported. `q` quits outside input fields; Ctrl+C cancels with exit code 130. Unsaved edits are not written automatically.

The TUI requires terminal stdin and stdout. Without a terminal, an invocation without arguments returns exit code 2 with a stderr diagnostic and empty stdout. Use `--help` for text help or a subcommand for scripts. Existing JSON commands do not start the TUI. `--config PATH` or `MARINADOREVIEW_CONFIG` selects the same config v1 file used by review commands.

To create an example configuration without the TUI:

```sh
marinadoreview config init
```

This creates a private configuration under `os.UserConfigDir()/marina-the-reviewer/config.json`. On macOS, that is normally `~/Library/Application Support/marina-the-reviewer/config.json`. `--config PATH` or `MARINADOREVIEW_CONFIG` selects another file. Project configuration is not discovered automatically.

Replace the example endpoints and models. A configuration can hold multiple profiles; routing selects one primary and one optional fallback:

```json
{
  "version": 1,
  "routing": {
    "primary": "cloud",
    "fallback": "backup",
    "fallbackOn": ["network_error", "timeout", "rate_limit", "server_error"]
  },
  "profiles": {
    "cloud": {
      "backend": "openai-compatible",
      "baseUrl": "https://provider.example/v1",
      "model": "review-model",
      "apiKeyEnv": "REVIEW_API_KEY",
      "contextLength": 131072,
      "responseFormat": "json_schema",
      "outputTokenParameter": "max_tokens",
      "maximumAttempts": 2,
      "timeoutSeconds": 300
    },
    "backup": {
      "backend": "openai-compatible",
      "baseUrl": "https://backup-provider.example/v1",
      "model": "backup-review-model",
      "apiKeyEnv": "BACKUP_REVIEW_API_KEY",
      "contextLength": 65536,
      "responseFormat": "json_object",
      "outputTokenParameter": "max_completion_tokens"
    }
  }
}
```

Each profile accepts exactly one of `apiKey`, `apiKeyEnv`, or `apiKeyFile`. Configuration with literal tokens and token files must have private permissions (`0600`). `config init` creates its directory with `0700` and preserves an existing file. Environment references work well in CI; private token files avoid command-line token exposure.

Other settings include `stream`, `maximumOutputTokens`, `maximumConcurrentRequests`, and `extraParameters`. Execution is currently serial, so each concurrency limit is respected. Protected request fields cannot be replaced through `extraParameters`. Remote API endpoints require HTTPS; loopback HTTP is supported for local models. HTTP redirects are not followed. Use `json_object` for servers without strict JSON Schema support.

## Review a change

```sh
marinadoreview review --repo . --base origin/dev --head HEAD \
  --profile cloud --fallback-profile backup \
  --run-dir ./review-run/my-change --output result.json
```

`--base` identifies the target tip. The CLI compares its merge-base with the pinned head and retains both comparison base and target tip. Update local refs before review if needed; no fetch runs automatically.

For exact replay, supply all three commit references:

```sh
marinadoreview prepare --repo . \
  --base-sha BASE_COMMIT --start-sha START_COMMIT --head-sha HEAD_COMMIT \
  --output snapshot.json
```

The separate steps support inspection and resume:

```sh
marinadoreview prepare --repo . --base origin/dev --head HEAD --output snapshot.json
marinadoreview plan --input snapshot.json --profile cloud --output plan.json
marinadoreview run --input plan.json --run-dir ./review-run/my-change --output result.json
```

Repeat the last command with the same plan and directory to reuse completed results. The private journal records actual provider/model/role, request hashes, attempts, usage, lineage and model outputs. API keys are excluded from plans, journals, results and events. A plan has a one-hour total execution deadline, including resume, plus a timeout for each request.

Source, prompt, routing and budgets are fixed in a plan. Use `plan --input plan.json ...` to change them. `run` accepts credential overrides but rejects provider/model/routing/prompt/budget overrides. Changing the endpoint does not implicitly reuse its old token.

### Execution archives

An integration can retain the native result and a separate versioned execution archive:

```sh
marinadoreview run --input plan.json --run-dir ./review-run/my-change \
  --output result.json --archive-output archive.json
marinadoreview archive --run-dir ./review-run/my-change --output recovered-archive.json
```

`capabilities --json` reports `executionArchiveVersion: 1`. The archive has kind `ReviewExecutionArchive` and includes the native result, source commit IDs, the semantic plan hash, the SHA-256 of the saved `plan.json` bytes, work lineage, prepared inputs, candidate audit and each attempt. Attempts include a physical request ID shared with token statistics, the actual HTTP request and response payloads, model output as a string, usage, finish reason and safe diagnostics. Invalid or truncated model text remains a string. Credentials are redacted, including JSON-escaped values. A bounded response prefix is marked with `responsePayloadTruncated` when the full body is unavailable.

The `archive` command reads a saved run without provider configuration or an AI request. It requires the run to be unlocked and rejects a changed plan or corrupt state. It can recover the native result when `result.json` was not written. A request interrupted before its final state was saved is reported as `unknown`; export does not retry it. Use the same saved plan and run directory to resume execution. Running `review` again creates a new plan and new review IDs.

Archives and run state are private files with a 128 MiB limit. Snapshots, plans, inputs and native results keep the 64 MiB document limit. An oversized archive fails explicitly and preserves an existing output file. Archive output must be separate from input, result, event and run-state files. Keep the plan and run directory for recovery. Runs made before archive support can still resume, but export fails if an earlier attempt has no captured archive data. The existing native `ReviewResult` format and `marina-v3` export remain unchanged.

Primary overrides are available for one-shot reviews:

```sh
marinadoreview review --repo . --base HEAD~1 --no-fallback \
  --base-url https://provider.example/v1 --model review-model \
  --api-key-env REVIEW_API_KEY --output result.json
```

`--api-key TOKEN` also works for one-shot `review` and credential overrides during `run`. Saved plans require an environment, file or profile reference. Primary overrides leave fallback settings and credentials independent.

## Recovery

After bounded retries, network errors, timeouts, HTTP 429 and HTTP 5xx can switch an unfinished batch to the chosen fallback. Its budget is recalculated and source can be split. Completed batches are retained; other configured profiles are never tried automatically.

Authentication errors, invalid requests, invalid model output, rejected findings and cancellation do not trigger fallback. Invalid output can receive one correction. Context overflow can split a batch with replan depth limited to three. A correction that cannot fit the reserved context uses the same bounded split before sending a request. Switching providers preserves those counters. Resume does not repeat a request with an uncertain outcome after interruption; inspect the journal and start a fresh run if another request is wanted.

## Token usage

```sh
marinadoreview usage
marinadoreview usage --period 7d
marinadoreview usage --period 30d --json
marinadoreview usage --period 90d
```

Without `--period`, the command shows all three rolling windows: 7, 30 and 90 days. Each window includes code review statistics and API token usage by normalized provider endpoint. Several profiles of one endpoint are combined; the JSON report includes their profile and model names. Windows use the UTC request or review start time, include their lower boundary, and exclude future records.

In the TUI (`marinadoreview` without arguments), open **Usage** from the home menu. Press `1`, `2` or `3` to select 7, 30 or 90 days, or use Left/Right and Tab. Press `r` to refresh, use arrows, PgUp/PgDn or Home/End to scroll, and Esc to return. Both views use the same report and require no provider configuration. Viewing statistics sends no AI requests, but can import old history and maintain the local database.

Token counters sum API-reported input, output and total tokens. If total is absent but both input and output are present, their sum is used. Source preparation estimates are not spent tokens. Every retry, fallback, correction and TUI connection test is a separate API attempt; model listing and offline commands add no usage. `UNKNOWN` includes unfinished requests without usage; `PARTIAL` means some token fields are missing. `?` means unavailable, rather than zero. Numeric sums include only known values. Failed or invalid answers can still consume tokens. Interrupted streams may lose the final usage event. OpenAI streaming requests ask for usage unless explicit `stream_options` are configured; arbitrary compatible endpoints retain their settings and may need `extraParameters.stream_options.include_usage` enabled. Groq streaming metadata is also supported.

A code review is one logical `runId/passId`. Retry, fallback and resume update that review; they do not create another one. Reports show complete, partial, failed, canceled and unfinished reviews, checked blocks, finding counts by severity and average duration of finished reviews. Duration is wall-clock time from the first execution, including pauses before resume. Findings that trigger `--fail-on` do not make a completed review technically failed. An abrupt process exit can leave an unfinished review with an unknown final outcome. Review statistics begin with this update; old token records cannot reconstruct them.

Statistics are stored in `os.UserConfigDir()/marina-the-reviewer/stats.sqlite3`. On macOS this is `~/Library/Application Support/marina-the-reviewer/stats.sqlite3`. `MARINADOREVIEW_DB_FILE` selects another SQLite file, independently of `--config`. The parent directory must be private and owned by the current user; newly created application directories use `0700`, and database, lock and SQLite sidecar files use `0600`. No database is created for empty-history queries or offline commands. The database stores counts and request/review metadata without API keys, prompts, source code, finding text or model output. History is local; provider billing and other applications are not queried.

History is retained for **90 days**. Expired rows are removed during storage access, with maintenance at most once per day. If the CLI is closed, cleanup runs on the next access. Free pages are reused and reclaimed incrementally; database size depends on activity within the retained window. This cleanup does not delete provider configurations, review output, or `review-run` files needed for resume.

The old default `usage.jsonl` is imported once, retaining only the last 90 days of token history. Identical requests are not counted twice. A validated incomplete final fragment is excluded with a warning; corrupt complete records stop migration and preserve the source. The default journal is retired only after a verified import and an unchanged-file check. Explicitly selected external sources are preserved. Close older CLI versions before migration; concurrent old JSONL writers and new SQLite writers are not supported.

The old `MARINADOREVIEW_USAGE_FILE` variable is deprecated: by itself it selects the import source, with the SQLite file created beside it as `<old-path>.sqlite3`. If both variables are set, `MARINADOREVIEW_DB_FILE` selects SQLite and the old variable selects the import source. A new database override alone does not import the personal default history. Update CI volumes and variables to retain the SQLite database.

A recording failure leaves the review running and emits a stderr warning after the command or TUI closes. A failed statistics read returns an error rather than a report of zero usage. The JSON report retains its existing token fields and adds `reviews` to each period, `reviewTrackingSince` and `retentionDays`; Marina review document formats are unchanged.

## Prompts and task context

The built-in policy combines local code and architecture review:

```sh
marinadoreview prompts show --builtin review
marinadoreview plan --input snapshot.json --system-prompt-file policy.txt --output plan.json
```

`--system-prompt TEXT` is also supported. The technical batch protocol remains attached so output can be validated. Conflicting legacy protocol instructions are rejected. `prepare` and `review` accept `--task-file task.json`; this is context only, with no Jira or GitLab network access.

## Marina compatibility

The baseline is Marina dev commit `acd37b498f1c9367b4044e8f9d5ad55b90617e59`, protocol v3, validation policy v2. Native documents include schema version, producer, baseline and schema digests.

Offline validation accepts a `SignificantReviewPreparedBatch` plus raw model JSON or a `ReviewModelOutputArchive`:

```sh
marinadoreview validate --input prepared-batch.json --model-output model-output.json \
  --format marina-v3 --run-id RUN_UUID --pass-id PASS_UUID --cluster-id CLUSTER_UUID \
  --output validated.json
```

For `shared-review-document-v1`, provide its document explicitly:

```sh
marinadoreview compat import --input marina-batch.json --document marina-document.json \
  --output document.json
marinadoreview plan --input document.json --profile cloud --output plan.json
marinadoreview compat export --input result.json --format marina-v3 --output marina-result.json
```

Import also accepts a bundle with `batch` or `plan` and `document`. It checks source, manifest and graph identity. It never follows artifact paths or reads a Marina database. Re-planning an imported document is explicit; local fallback defaults are not silently attached to an imported fixed request.

`compat export --input plan.json --format marina-v3` exports a prepared plan; add `--batch 1` for a single prepared batch. Raw model archives and stored batch results round-trip through a labeled `MarinaArtifact` wrapper. Importing a stored result does not claim fresh source validation; `validate` performs that check with its prepared input.

Validation enforces exact checked-block coverage, evidence quotes/ranges/revisions, graph references, required source context, changed-line anchors and inline positions. Rejections and duplicate suppression remain in the candidate audit. Fingerprints and .NET GUID derivation preserve the baseline behavior. A complete review can still report defects.

See [compatibility notes](docs/compatibility.md) for tested parity and parser differences. `schemas/` contains JSON shape schemas. Go validation adds source integrity and semantic checks beyond those schemas.

## Analysis limits

Java uses a lexical parser, with ambiguous dependencies left unresolved. It does not reproduce Marina AST symbol IDs, inherited/type-based overload resolution or move heuristics. Unsupported Java and non-Java source use explicit block fallbacks. Missing blobs, binary/LFS content and submodules produce limitations and incomplete coverage when source is unavailable.

Token counts are conservative estimates, not provider tokenizer results. Planning reserves schema/output/safety capacity, and bounded replanning handles server context rejection. Large splits preserve primary lines without guessing a base/head alignment. Codex and Claude process executors are outside this HTTP release.

## Output and status

`--input -` reads stdin; `--output -` writes one JSON document to stdout. Diagnostics use stderr. `--output PATH` writes atomically; `--events-file PATH` writes separate JSONL. Only one input may consume stdin. Documents default to a 64 MiB limit, reducible with `--max-document-bytes`; individual Git source objects are limited to 4 MiB. Private run directories use locks and reject a changed plan or corrupt state. Keep source artifacts private when their code is sensitive.

| Exit | Meaning |
| --- | --- |
| 0 | Complete technical review or successful utility command |
| 2 | Invalid arguments or configuration |
| 3 | Partial/incomplete review |
| 4 | AI failure without a completed batch |
| 5 | Invalid contract, source, state or output |
| 10 | Complete review reached `--fail-on warning\|error\|critical` threshold |
| 130 | User cancellation |

## Development

```sh
make verify
go run scripts/generate_schemas.go
```

Tests use synthetic fixtures, temporary Git repositories and local fake AI servers. No token, paid request, Marina service or database is needed. See [RELEASING.md](RELEASING.md) for publication. MIT license: [LICENSE](LICENSE).
