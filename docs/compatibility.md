# Compatibility

Baseline: `acd37b498f1c9367b4044e8f9d5ad55b90617e59`. Protocol: 3. Validation policy: 2. `internal/review/assets/provenance.json` records embedded prompt/schema hashes. Native documents carry baseline and protocol/schema digests separately from source identity.

`testdata/compat/stages.json` contains synthetic inputs and expected outputs from the actual C# application/domain assemblies. Its manifest records source/fixture hashes and expected differences. Normal Go tests read frozen fixtures and need no C# installation or private checkout.

Recorded comparisons cover:

- Effective prompt, response schema and provider output budgets.
- Compact wire serialization, graph projection and wire hashes.
- Twelve validator cases, including identities, fingerprints, evidence, rejection and audit.
- Java source selection and changed-line disposition in a synthetic case.
- Whole-batch dependencies, both revisions of unchanged callees and structural context.

This boundary does not imply exhaustive source-parser equivalence. Native Java analysis uses lexical tokens. Symbol IDs, inherited/type-based overload resolution, move heuristics and exact split cuts can differ. Ambiguous dependencies stay unresolved. Unsupported constructs carry limitations, and non-Java source uses a block fallback. Splits retain primary lines separately for each revision where alignment is not proven.

Stored DTO enums are numeric; the AI wire projection uses string values. Imports accept camelCase and PascalCase C# DTO properties. Shared restoration checks base/start/head, manifest and graph identity. A standalone document does not expose its external artifact UUID, so content checks cannot prove database ownership of that UUID.

To regenerate with .NET 10 and the pinned source:

```sh
python3 scripts/export_compat_fixtures.py --source /path/to/petrel-infrastructure --output testdata/compat
make verify
```

The exporter uses `git archive` at the baseline, then builds application/domain code and the local harness in a temporary directory. It does not build the original checkout, read its configuration/database, or copy real review payloads. No production exporter changes are needed.

Future Marina integration and protocol upgrades are separate work. Change the baseline and regenerate reviewed synthetic fixtures before claiming another version is compatible.
