# Go source analysis

`prepare` and `plan` support Go source without an installed Go SDK in the reviewed project's environment. The CLI contains the parser. It reads pinned Git objects and does not run `go list`, a language server, module downloads, project builds, generators, hooks or filters.

## Selected source

The snapshot includes changed files, unchanged local `.go` files and `go.mod` / `go.work` metadata from both revisions. Vendor source is not added automatically. A changed vendor file still receives review coverage. Symlink context files are not followed.

The analyzer uses Go AST boundaries for functions, receiver methods, types and grouped declarations. Doc comments stay with declarations. Grouped constants preserve `iota` context. Physical source coordinates remain valid with CRLF, Unicode and `//line` directives. Functions share the existing protocol's callable node kind with methods.

The local package index resolves same-package functions, local package imports, import aliases, concrete receiver methods, type aliases and interface method contracts. A method can be declared in a different file from its receiver type. Interface targets describe the contract; they do not prove a concrete runtime implementation.

Direct callers and callees enter the selected review graph. Required local types, values and initializer source enter structural context. Dependency bodies are selected and packed within the existing provider budget. The full selected graph, available dependency source and the graph/source passed to one batch are separate views. Incoming calls from unchanged callers follow the existing batch protocol.

Go package declarations and import groups stay intact during packing, including alias and side-effect imports. Unsupported or malformed syntax falls back to source regions and keeps an explicit limitation. Source coverage does not imply complete dependency resolution.

## Boundaries

- Only source in the pinned repository is available. Local workspace members and unversioned local replacements can connect modules inside that tree. External paths and version-dependent replacements carry limitations.
- Standard library and external package bodies are not read from the machine or a module cache. Calls without a proven local target remain unresolved.
- Interface dispatch, reflection, embedded-method promotion and unknown function values do not produce invented implementation targets. Local type inference has bounded depth and work.
- Build constraints and filename OS/architecture suffixes use a source-union policy. The host OS and environment do not choose a build. Conditional targets remain uncertain when their constraints are not established by the source comparison.
- Each Go parser input is limited to 4 MiB. Additional Go context source has a 32 MiB snapshot budget across both revisions. Existing document and provider limits still apply. Missing context produces limitations.
- Parser calls are synchronous and bounded by input size. Analysis checks cancellation before and after parsing and inside its own graph and AST walks.

`capabilities --json` reports `goAstAnalysis`, `goDependencyContext` and `sourceSelectionVersion`. Source selection version 3 enables Go by default. An existing saved document keeps its original source policy and hashes during resume.

## Offline verification

The default suite includes source fixtures from this tool, with their origin and content hashes in `testdata/go-self-review/manifest.json`. The small fixture repository exercises these dependencies:

- `sortDependencyAdditionsContext` → `newDependencyPriorityIndexContext` in the same package.
- `engine.BuildContext` → `review.AnalyzeContext` across a local import.
- Receiver methods and the declarations of their named types.

The integration test creates a temporary Git repository, prepares pinned source, builds a plan and checks the dependency bodies in the actual batch. A local fake HTTP server returns a finding with a real changed Go source quote. The test validates that finding, saves and loads an artifact bundle, exports an archive and resumes without repeating the request. It uses no real credentials or paid requests.

Run the focused checks from this tool's checkout:

```sh
go test ./internal/review -run '^TestGo' -count=1
go test ./internal/engine -run '^TestGoSelfReview' -count=1
go test ./internal/review -run '^$' -fuzz '^FuzzGoSourceAnalysis$' -fuzztime 20s
go test ./internal/review -run '^$' -bench '^BenchmarkGoPackageIndex$' -benchmem
```

These checks assess source preparation, dependency selection, evidence and persistence. They do not assess the quality of a paid model's review.
