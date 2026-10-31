package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func goTestSnapshot(path, before, after string, changedLine int, contextSources map[string]string) Snapshot {
	snapshot := mrGraphSnapshot(path, before, after, changedLine)
	contextSources["go.mod"] = "module example.test/project\n\ngo 1.27\n"
	for path, content := range contextSources {
		snapshot.Files = append(snapshot.Files, FileSnapshot{OldPath: path, NewPath: path, Operation: "context", Base: &SourceSnapshot{Path: path, Revision: Base, Content: content}, Head: &SourceSnapshot{Path: path, Revision: Head, Content: content}})
	}
	return snapshot
}

func goTestCall(t *testing.T, document Document, name string) GraphEdge {
	t.Helper()
	for _, edge := range document.Graph.Edges {
		if edge.Kind == "call" && edge.Revision == Head && edge.InvokedName != nil && *edge.InvokedName == name {
			return edge
		}
	}
	t.Fatalf("missing Go call %s", name)
	return GraphEdge{}
}

func TestGoFunctionsSelectLocalDependencies(t *testing.T) {
	before := "package sample\n\nfunc Execute() int {\n return helper()\n}\n"
	snapshot := goTestSnapshot("sample/main.go", before, strings.Replace(before, "helper()", "helper() + 1", 1), 4, map[string]string{
		"sample/helper.go": "package sample\n\nfunc helper() int { return 1 }\nfunc unused() int { return 0 }\n",
		"sample/caller.go": "package sample\n\nfunc Caller() int { return Execute() }\n",
	})
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) != 1 || document.Blocks[0].Kind != "method" || !document.Coverage.IsComplete {
		t.Fatalf("Go function was not selected: blocks=%+v coverage=%+v", document.Blocks, document.Coverage)
	}
	call := goTestCall(t, document, "helper")
	if call.Resolution != 0 || len(call.TargetIDs) != 1 {
		t.Fatalf("local helper is unresolved: %+v", call)
	}
	context := ""
	for _, source := range document.DependencyContext {
		context += source.Content
	}
	if !strings.Contains(context, "func helper") || strings.Contains(context, "func unused") {
		t.Fatalf("wrong dependency bodies: %s", context)
	}
	if err := ValidateDocument(document); err != nil {
		t.Fatal(err)
	}
}

func TestGoImportsResolveDeclaredPackageNamesAndAliases(t *testing.T) {
	before := "package app\nimport service \"example.test/project/implementation\"\nfunc Execute() int {\n return service.Load()\n}\n"
	snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "service.Load()", "service.Load() + 1", 1), 4, map[string]string{
		"implementation/service.go": "package unusual\nfunc Load() int { return 1 }\n",
		"other/service.go":          "package other\nfunc Load() int { return 9 }\n",
	})
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	call := goTestCall(t, document, "Load")
	if call.Resolution != 0 || len(call.TargetIDs) != 1 {
		t.Fatalf("import alias was not resolved: %+v", call)
	}
	for _, node := range document.Graph.Nodes {
		if node.ID == call.TargetIDs[0] && node.Path != "implementation/service.go" {
			t.Fatalf("resolved to the wrong package: %+v", node)
		}
	}
}

func TestGoReceiverOutsideTypeRangeAndValueDependencies(t *testing.T) {
	before := "package app\nfunc Execute(service *Service) int {\n return service.Load() + Limit\n}\n"
	snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "+ Limit", "+ Limit + 1", 1), 3, map[string]string{
		"app/service.go": "package app\ntype Service struct { Value int }\nfunc (s *Service) Load() int { return s.Value }\nconst Limit = 10\n",
	})
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	call := goTestCall(t, document, "Load")
	if call.Resolution != 0 || len(call.TargetIDs) != 1 {
		t.Fatalf("receiver method was not resolved: %+v", call)
	}
	allContext := ""
	for _, block := range document.Blocks {
		for _, source := range block.StructuralContext {
			allContext += source.Content
		}
	}
	if !strings.Contains(allContext, "type Service struct") || !strings.Contains(allContext, "const Limit") {
		t.Fatalf("type or value declaration is missing: %s", allContext)
	}
	if err := ValidateDocument(document); err != nil {
		t.Fatal(err)
	}
}

func TestGoShadowingAndInterfaceDoNotInventConcreteCalls(t *testing.T) {
	before := "package app\nimport remote \"example.test/project/service\"\nfunc Execute(remote interface{ Load() int }, f func() int) int {\n return remote.Load() + f()\n}\n"
	snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "+ f()", "+ f() + 1", 1), 4, map[string]string{
		"service/service.go": "package service\nfunc Load() int { return 1 }\n",
		"app/other.go":       "package app\nfunc f() int { return 0 }\n",
	})
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Load", "f"} {
		call := goTestCall(t, document, name)
		if call.Resolution == 0 || len(call.TargetIDs) != 0 {
			t.Fatalf("shadowed dynamic call became concrete: %+v", call)
		}
	}
}

func TestGoPhysicalPositionsGenericsAndGroupedConstants(t *testing.T) {
	before := "package app\n//line fake.go:9000\n// Identity returns its input.\nfunc Identity[T any](value T) T {\n return value\n}\nconst (\n A = iota\n B\n)\n"
	snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "return value", "return value // changed", 1), 5, map[string]string{})
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) != 1 || document.Blocks[0].Kind != "method" || document.Blocks[0].Head.StartLine != 2 || document.Blocks[0].Head.EndLine != 6 {
		t.Fatalf("physical AST bounds are wrong: %+v", document.Blocks)
	}
	if err := ValidateDocument(document); err != nil {
		t.Fatal(err)
	}
	snapshot = goTestSnapshot("app/main.go", before, strings.Replace(before, "B\n", "B = 7\n", 1), 9, map[string]string{})
	document, err = Analyze(snapshot)
	if err != nil || len(document.Blocks) != 1 || !strings.Contains(document.Blocks[0].Head.Content, "A = iota") {
		t.Fatalf("grouped constants were cut apart: %+v %v", document.Blocks, err)
	}
}

func TestGoSignatureChangeKeepsCounterpart(t *testing.T) {
	before := "package app\nfunc Execute(value int) int {\n return value\n}\n"
	after := strings.ReplaceAll(before, "int", "int64")
	document, err := Analyze(goTestSnapshot("main.go", before, after, 2, map[string]string{}))
	if err != nil || len(document.Blocks) != 1 || document.Blocks[0].Base == nil || document.Blocks[0].Head == nil {
		t.Fatalf("signature change lost its counterpart: %+v %v", document.Blocks, err)
	}
}

func TestGoSameLineDeclarationsHaveOneOwner(t *testing.T) {
	before := "package app\nfunc A() int { return 1 }; func B() int { return A() }\n"
	after := strings.Replace(before, "return 1", "return 2", 1)
	document, err := Analyze(goTestSnapshot("main.go", before, after, 2, map[string]string{}))
	if err != nil || !document.Coverage.IsComplete {
		t.Fatalf("one-line declarations lost coverage: %+v %v", document.Coverage, err)
	}
	if err := ValidateDocument(document); err != nil {
		t.Fatal(err)
	}
}

func TestGoPackageNamesAndSwitchScopes(t *testing.T) {
	before := "package app\nimport unusual \"example.test/project/service\"\nfunc Execute() int {\n switch unusual := 1; unusual { case 1: }\n return unusual.Load()\n}\n"
	snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "unusual.Load()", "unusual.Load() + 1", 1), 5, map[string]string{
		"service/service.go": "package unusual\nfunc Load() int { return 1 }\n",
	})
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	call := goTestCall(t, document, "Load")
	if call.Resolution != 0 || len(call.TargetIDs) != 1 {
		t.Fatalf("switch variable escaped its scope: %+v", call)
	}
	before = strings.Replace(before, "import unusual ", "import ", 1)
	document, err = Analyze(goTestSnapshot("app/main.go", before, strings.Replace(before, "unusual.Load()", "unusual.Load() + 1", 1), 5, map[string]string{
		"service/service.go": "package unusual\nfunc Load() int { return 1 }\n",
	}))
	if err != nil || goTestCall(t, document, "Load").Resolution != 0 {
		t.Fatalf("declared import package name was ignored: %v", err)
	}
}

func TestGoLocalWorkspaceAndReplacement(t *testing.T) {
	before := "package app\nimport svc \"example.test/service\"\nfunc Execute() int {\n return svc.Load()\n}\n"
	for _, workspace := range []bool{false, true} {
		snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "svc.Load()", "svc.Load() + 1", 1), 4, map[string]string{
			"service/go.mod":     "module example.test/service\n\ngo 1.27\n",
			"service/service.go": "package service\nfunc Load() int { return 1 }\n",
		})
		if workspace {
			addMRGraphContext(&snapshot, "go.work", "go 1.27\nuse (\n .\n ./service\n)\n")
		} else {
			for index := range snapshot.Files {
				if snapshot.Files[index].NewPath == "go.mod" {
					snapshot.Files[index].Base.Content += "replace example.test/service => ./service\n"
					snapshot.Files[index].Head.Content += "replace example.test/service => ./service\n"
				}
			}
		}
		document, err := Analyze(snapshot)
		if err != nil || goTestCall(t, document, "Load").Resolution != 0 {
			t.Fatalf("local module connection was lost (workspace=%t): %v", workspace, err)
		}
	}
}

func TestGoBuiltinsDoNotHidePackageFunctions(t *testing.T) {
	before := "package app\nfunc Execute() int {\n return max(1, 2)\n}\n"
	document, err := Analyze(goTestSnapshot("main.go", before, strings.Replace(before, "max(1, 2)", "max(2, 3)", 1), 3, map[string]string{
		"helper.go": "package app\nfunc max(a, b int) int { return a + b }\n",
	}))
	if err != nil || goTestCall(t, document, "max").Resolution != 0 {
		t.Fatalf("package declaration was replaced by a builtin: %v", err)
	}
}

func TestGoInterfaceContractAndTypeAlias(t *testing.T) {
	for _, test := range []struct{ parameter, types string }{
		{"Reader", "type Reader interface { Load() int }\n"},
		{"Alias", "type Service struct{}\ntype Alias = Service\nfunc (s Service) Load() int { return 1 }\n"},
	} {
		before := "package app\nfunc Execute(service " + test.parameter + ") int {\n return service.Load()\n}\n"
		document, err := Analyze(goTestSnapshot("main.go", before, strings.Replace(before, "service.Load()", "service.Load() + 1", 1), 3, map[string]string{
			"types.go": "package app\n" + test.types,
		}))
		if err != nil || len(goTestCall(t, document, "Load").TargetIDs) != 1 {
			t.Fatalf("local contract or alias was lost (%s): %v", test.parameter, err)
		}
		if err := ValidateDocument(document); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGoSingleConditionalImplementationIsNotProven(t *testing.T) {
	before := "package app\nfunc Execute() int {\n return helper()\n}\n"
	document, err := Analyze(goTestSnapshot("main.go", before, strings.Replace(before, "helper()", "helper() + 1", 1), 3, map[string]string{
		"helper_linux.go": "//go:build linux\n\npackage app\nfunc helper() int { return 1 }\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if call := goTestCall(t, document, "helper"); call.Resolution == 0 {
		t.Fatalf("conditional source was treated as unconditional: %+v", call)
	}
}

func TestGoInitializersKeepReferencedSource(t *testing.T) {
	before := "package app\nvar Value = create()\n"
	document, err := Analyze(goTestSnapshot("main.go", before, strings.Replace(before, "create()", "create() + 1", 1), 2, map[string]string{
		"helper.go": "package app\nfunc create() int { return Limit }\nconst Limit = 10\n",
	}))
	if err != nil || !document.Coverage.IsComplete {
		t.Fatalf("initializer lost coverage: %+v %v", document, err)
	}
	context := ""
	for _, block := range document.Blocks {
		for _, source := range block.StructuralContext {
			context += source.Content
		}
	}
	if !strings.Contains(context, "func create()") || !strings.Contains(context, "const Limit") {
		t.Fatalf("initializer dependency source is missing: %s", context)
	}
}

func TestGoVariantsAndMixedLanguagesStayUncertain(t *testing.T) {
	before := "package app\nfunc Execute() int {\n return helper()\n}\n"
	snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "helper()", "helper() + 1", 1), 3, map[string]string{
		"app/helper_linux.go":  "package app\nfunc helper() int { return 1 }\n",
		"app/helper_darwin.go": "package app\nfunc helper() int { return 2 }\n",
		"Other.java":           "class Other { int helper() { return 9; } }\n",
	})
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	call := goTestCall(t, document, "helper")
	if call.Resolution != 1 || len(call.TargetIDs) != 2 {
		t.Fatalf("platform variants became one proven implementation: %+v", call)
	}
	for _, node := range document.Graph.Nodes {
		if strings.HasSuffix(node.Path, ".java") {
			t.Fatalf("Java symbols leaked into Go dependencies: %+v", node)
		}
	}
}

func TestGoPrepareLoadsPinnedLocalContext(t *testing.T) {
	repository := t.TempDir()
	gitTest(t, repository, "init", "-q")
	for path, content := range map[string]string{
		"go.mod": "module example.test/project\n\ngo 1.27\n", "main.go": "package app\nfunc Execute() int { return helper() }\n",
		"helper.go": "package app\nfunc helper() int { return 1 }\n", "unrelated.py": "value = 1\n",
	} {
		if err := os.WriteFile(filepath.Join(repository, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitTest(t, repository, "add", ".")
	gitTest(t, repository, "commit", "-qm", "base")
	base := gitTest(t, repository, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repository, "main.go"), []byte("package app\nfunc Execute() int { return helper() + 1 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "commit", "-qam", "head")
	if err := os.WriteFile(filepath.Join(repository, "helper.go"), []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status := gitTest(t, repository, "status", "--porcelain=v1")
	snapshot, err := Prepare(t.Context(), PrepareOptions{Repository: repository, Base: base, ExactBase: true})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, file := range snapshot.Files {
		paths[file.NewPath] = true
		if file.NewPath == "helper.go" && (file.Head == nil || strings.Contains(file.Head.Content, "dirty")) {
			t.Fatal("context was read from the working tree")
		}
	}
	if !paths["helper.go"] || !paths["go.mod"] || paths["unrelated.py"] {
		t.Fatalf("wrong pinned Go context files: %v", paths)
	}
	if got := gitTest(t, repository, "status", "--porcelain=v1"); got != status {
		t.Fatal("prepare modified the reviewed repository")
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestGoImportGroupsRemainWholeDuringPacking(t *testing.T) {
	imports := CodeSlice{Path: "main.go", Revision: Head, StartLine: 1, EndLine: 5, SymbolStartLine: 1, SymbolEndLine: 5, IsComplete: true, Content: "package app\nimport (\n svc \"example.test/project/service\"\n _ \"example.test/project/registration\"\n)"}
	primary := CodeSlice{Path: "main.go", Revision: Head, StartLine: 6, EndLine: 6, SymbolStartLine: 6, SymbolEndLine: 6, IsComplete: true, Content: "func Execute() { svc.Load() }"}
	block := Block{ID: "b1", FilePath: "main.go", Head: &primary, HeadExists: true, StructuralContext: []CodeSlice{imports}}
	document := Document{SelectionVersion: 3, Blocks: []Block{block}, DeclarationContext: []CodeSlice{imports}}
	batch := Batch{Blocks: []Block{block}}
	index := newContextIndex(document)
	for _, packed := range []Batch{index.selectImports(batch), index.normalizeTrial(batch)} {
		if len(packed.Blocks[0].StructuralContext) != 1 || packed.Blocks[0].StructuralContext[0].Content != imports.Content {
			t.Fatalf("Go import group was cut: %+v", packed)
		}
	}
}

func TestGoMalformedSourcePreservesCoverageAndCancellation(t *testing.T) {
	before := "package app\nfunc Execute( {\n"
	document, err := Analyze(goTestSnapshot("main.go", before, before+"broken\n", 2, map[string]string{}))
	if err != nil || !document.Coverage.IsComplete || len(document.Blocks) == 0 {
		t.Fatalf("malformed source lost changed lines: %+v %v", document, err)
	}
	if !strings.Contains(strings.Join(document.Limitations, "\n"), "Go") {
		t.Fatal("malformed Go source has no parser limitation")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := AnalyzeContext(ctx, goTestSnapshot("main.go", before, before, 2, map[string]string{})); err == nil {
		t.Fatal("cancelled Go analysis continued")
	}
}
