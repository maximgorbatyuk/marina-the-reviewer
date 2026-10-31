# Native artifact bundles

`capabilities --json` reports `reviewArtifactBundleVersion: 2` and
`reviewResumeVersion: 1`. These capabilities apply to the opt-in commands below.
The existing `prepare`, `plan`, `run` and version 1 execution archive contracts
remain available. Bundle version 2 does not change model protocol 3 or validation
policy 2.

```sh
marinadoreview review --repo ./objects.git \
  --base-sha BASE --start-sha START --head-sha HEAD \
  --config provider.json --no-fallback --system-prompt-file policy.txt \
  --task-file task.json --artifacts-dir ./review-bundle
marinadoreview archive --artifacts-dir ./review-bundle
marinadoreview archive --artifacts-dir ./review-bundle --export-dir ./exported-bundle
```

The first command performs preparation, analysis, planning, inference, validation
and aggregation in one process. It writes `bundle.json`, a physical `plan.json`,
immutable `content/` parts and private `execution/` checkpoints. Standard output
contains the public bundle manifest. Standard error contains safe phase records
with wall and CPU times. The CLI does not fetch source or publish review comments.

## Identity and resume

The command requires all three pinned commit IDs. The saved invocation binds the
resolved repository path, those IDs, the exact policy and task hashes, normalized
provider/routing/budget snapshots and resource policy. Repeating the command with
a different input is rejected before HTTP. Credential values are excluded from
the fingerprint and artifacts; rotation of a referenced credential is allowed.
Literal `--api-key` is not accepted in bundle mode.

The same bundle reuses its saved plan and native run/pass IDs. A snapshot without
a plan can continue planning only when no execution checkpoint exists. An absent
or damaged dependency does not trigger a new preparation or inference operation.
A persisted `running` fence without its journal is an unknown outcome and blocks
automatic inference. An in-flight or interrupted request is exported as unknown
and is never resent by resume. Completed results are returned without HTTP.

The private root writer lock covers the full command and is separate from the
execution journal lock. Keep `bundle.json`, `plan.json`, `invocation.json`,
`content/` and `execution/` for resumable work. A public export contains the bundle,
physical plan and all content parts, but no provider configuration, credential
file, reviewed Git repository or private execution journal. It can be read,
validated and exported offline without the original private state. A completed
public bundle cannot cause a new inference when read with `review`.

## Public wire contract

`bundle.json` has kind `ReviewArtifactBundle`, schema version 2, producer and
compatibility metadata, invocation hash, native IDs, the three source SHAs,
selection version, semantic plan hash, physical plan-file SHA-256, status,
first-invocation time, deadline and resource policy. Optional `exporter` identifies
an offline export; the original producer and native result producer are retained.
Its `entries` map uses the keys `snapshot`, `document`, `plan`, `result` and
`archive`. Later entries can be absent when an earlier phase fails.

Each entry is a content reference:

```json
{
  "sha256": "SHA256_OF_ALL_UNCOMPRESSED_BYTES",
  "byteLength": 12345,
  "parts": [
    {
      "sha256": "SHA256_OF_PART_BYTES",
      "byteLength": 12345,
      "file": "content/SHA256_OF_PART_BYTES.bin"
    }
  ]
}
```

Concatenate the parts in order. Check each exact length and digest and then the
total length and digest. Part paths must have exactly the form above. Files and
directories must be private, owned and regular; symbolic links are rejected.
Parts contain raw bytes, not separately valid JSON documents. A UTF-8 character
or JSON token can cross a part boundary. The empty byte sequence has no parts and
the SHA-256 of an empty input.

The resolved entries are:

| Entry | Content |
| --- | --- |
| `snapshot` | Existing version 1 `ReviewSnapshot`, including full source files |
| `document` | Existing version 1 `ReviewDocument`, with one full graph and all retained source context |
| `plan` | Version 2 `ReviewPlan`; `documentRef` and prepared-batch references replace copies |
| `result` | Existing version 1 `ReviewResult`, including the complete aggregate and candidate audit |
| `archive` | Version 2 `ReviewExecutionArchive`, with work, attempt and output references |

A plan's prepared batches are references to `ReviewPreparedBatch` version 2. Each
contains `documentSha256` and `batch`, where the existing prepared batch retains
empty graph arrays and the full graph SHA. The reader checks the document hash,
source SHAs, manifest and graph identity before it restores the runtime graph.
The model's projected user prompt and input hash remain unchanged by this storage
encoding.

Archive works retain `workId`, parent/role, `preparedRef`, optional `resultRef`,
failure, superseded/in-flight state and recovery counters. Attempt metadata keeps
request ID, phase/number, provider/model, usage, finish reason, timing, code and
unknown status. `systemPromptRef`, `userPromptRef`, `schemaRef`,
`requestPayloadRef`, `responsePayloadRef` and `rawOutputRef` hold the corresponding
exact safe-redacted bytes. A diagnostic can have `rawResponseRef`. The `outputs`
array holds `{workId, attempt, outputRef}` for the normalized JSON used by native
validation, separately from the original model text. Equal byte contents share
parts; different representations remain distinct. Invalid model text stays text.

`planFileSha256` hashes the exact saved public `plan.json` bytes. `planHash` uses
SHA-256 of `review-plan-v2\n` followed by Go's compact JSON encoding of the version
2 plan. It is not the version 1 semantic hash or the model request input hash.
Resume does not rewrite a version 1 plan as version 2.

The generated [bundle schema](../schemas/review-artifact-bundle-v2.schema.json),
[plan schema](../schemas/review-plan-v2.schema.json),
[prepared-batch schema](../schemas/review-prepared-batch-v2.schema.json) and
[archive schema](../schemas/review-execution-archive-v2.schema.json) describe the
public shapes. Source ownership, content integrity and semantic validation remain
runtime checks.

## Resource policy

| Review flag | Default | Meaning |
| --- | ---: | --- |
| `--max-bundle-unique-bytes` | 1073741824 | Total unique bytes in immutable content parts |
| `--max-bundle-parts` | 4096 | Maximum unique content parts and parts in one reference |
| `--deadline-seconds` | 3600 | Total deadline from the first invocation, including CPU phases and resume |

These values are part of the saved invocation. `archive --artifacts-dir` uses the
saved policy without provider configuration. Each part has a fixed 4 MiB codec
bound; a logical document can cross the old 64/128 MiB file boundaries. Runtime
objects, model context/output tokens and the private metadata journal have their
own validation and resource checks. A storage limit does not remove source,
graph, coverage or audit fields. Errors name the phase, resource and actual and
allowed values. Existing outputs and committed checkpoints are retained on
failure. Unknown results require inspection and an explicit new run if another
paid request is wanted.
