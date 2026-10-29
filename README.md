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

Repeat the last command with the same plan and directory to reuse completed results. The private journal records actual provider/model/role, request hashes, attempts, usage, lineage and model outputs. Tokens are excluded from plans, journals, results and events. A plan has a one-hour total execution deadline, including resume, plus a timeout for each request.

Source, prompt, routing and budgets are fixed in a plan. Use `plan --input plan.json ...` to change them. `run` accepts credential overrides but rejects provider/model/routing/prompt/budget overrides. Changing the endpoint does not implicitly reuse its old token.

Primary overrides are available for one-shot reviews:

```sh
marinadoreview review --repo . --base HEAD~1 --no-fallback \
  --base-url https://provider.example/v1 --model review-model \
  --api-key-env REVIEW_API_KEY --output result.json
```

`--api-key TOKEN` also works for one-shot `review` and credential overrides during `run`. Saved plans require an environment, file or profile reference. Primary overrides leave fallback settings and credentials independent.

## Recovery

After bounded retries, network errors, timeouts, HTTP 429 and HTTP 5xx can switch an unfinished batch to the chosen fallback. Its budget is recalculated and source can be split. Completed batches are retained; other configured profiles are never tried automatically.

Authentication errors, invalid requests, invalid model output, rejected findings and cancellation do not trigger fallback. Invalid output can receive one correction. Context overflow can split a batch with replan depth limited to three. Switching providers preserves those counters. Resume does not repeat a request with an uncertain outcome after interruption; inspect the journal and start a fresh run if another request is wanted.

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
