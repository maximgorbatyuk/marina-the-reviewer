package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestGoUnrelatedPackagesDoNotChangeSelectedGraph(t *testing.T) {
	before := "package app\nfunc Execute() int {\n return helper()\n}\n"
	snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "helper()", "helper() + 1", 1), 3, map[string]string{
		"app/helper.go": "package app\nfunc helper() int { return 1 }\n",
	})
	want, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 100 {
		addMRGraphContext(&snapshot, fmt.Sprintf("aaa/package%d/other.go", index), "package other\nfunc helper() int { return 5 }\n")
	}
	got, err := Analyze(snapshot)
	if err != nil || want.Graph.Sha256 != got.Graph.Sha256 {
		t.Fatalf("unrelated Go packages changed the selected graph: %v", err)
	}
}

func TestGoLegacySelectionRetainsFallback(t *testing.T) {
	before := "package app\nfunc Execute() int {\n return helper()\n}\n"
	snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "helper()", "helper() + 1", 1), 3, map[string]string{
		"app/helper.go": "package app\nfunc helper() int { return 1 }\n",
	})
	legacy, err := analyzeWithSelection(snapshot, 2)
	if err != nil || len(legacy.Blocks) != 1 || legacy.Blocks[0].Kind != "file" || legacy.SelectionVersion != 2 {
		t.Fatalf("legacy selection was upgraded silently: %+v %v", legacy.Blocks, err)
	}
}

func TestGoSavedSelectionCanResumeWithoutUpgrading(t *testing.T) {
	before := "package app\nfunc Execute() int { return 1 }\n"
	snapshot := goTestSnapshot("main.go", before, strings.Replace(before, "return 1", "return 2", 1), 2, map[string]string{})
	document, err := AnalyzeSelectionContext(t.Context(), snapshot, 2)
	if err != nil || document.SelectionVersion != 2 || document.Blocks[0].Kind != "file" {
		t.Fatalf("saved policy did not retain fallback: %v", err)
	}
	if _, err := AnalyzeSelectionContext(t.Context(), snapshot, CurrentSelectionVersion+1); err == nil {
		t.Fatal("unknown saved selection was accepted")
	}
}

func TestGoCancellationDuringASTWalk(t *testing.T) {
	before := "package app\nfunc Execute() int {\n"
	for index := range 500 {
		before += fmt.Sprintf("v%d := helper()\n_ = v%d\n", index, index)
	}
	before += "return 1\n}\n"
	snapshot := goTestSnapshot("main.go", before, strings.Replace(before, "return 1", "return 2", 1), 1003, map[string]string{
		"helper.go": "package app\nfunc helper() int { return 1 }\n",
	})
	ctx := newReviewCountingContext(100)
	defer ctx.cancel()
	if _, err := AnalyzeContext(ctx, snapshot); !errors.Is(err, context.Canceled) {
		t.Fatalf("Go walk ignored cancellation: %v", err)
	}
}

func TestGoArchitectureSuffixRemainsConditional(t *testing.T) {
	before := "package app\nfunc Execute() int {\n return helper()\n}\n"
	document, err := Analyze(goTestSnapshot("main.go", before, strings.Replace(before, "helper()", "helper() + 1", 1), 3, map[string]string{
		"helper_amd64.go": "package app\nfunc helper() int { return 1 }\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if call := goTestCall(t, document, "helper"); call.Resolution == 0 {
		t.Fatal("architecture-specific implementation became unconditional")
	}
}

func TestGoWorkspaceReplacementOverridesModuleReplacement(t *testing.T) {
	before := "package app\nimport svc \"example.test/service\"\nfunc Execute() int {\n return svc.Load()\n}\n"
	snapshot := goTestSnapshot("main.go", before, strings.Replace(before, "svc.Load()", "svc.Load() + 1", 1), 4, map[string]string{
		"service/go.mod":     "module example.test/service\n\ngo 1.27\n",
		"service/service.go": "package service\nfunc Load() int { return 1 }\n",
		"go.work":            "go 1.27\nuse .\nreplace example.test/service => ./service\n",
	})
	document, err := Analyze(snapshot)
	if err != nil || goTestCall(t, document, "Load").Resolution != 0 {
		t.Fatalf("workspace replacement was ignored: %v", err)
	}
}

func TestGoSameLineTypeDoesNotOwnFreeFunction(t *testing.T) {
	before := "package app\ntype Service struct{}; func Execute() int { return 1 }\n"
	document, err := Analyze(goTestSnapshot("main.go", before, strings.Replace(before, "return 1", "return 2", 1), 2, map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range document.Graph.Nodes {
		if node.Name == "Execute" && node.DeclaringType != nil {
			t.Fatal("line overlap invented a Go receiver owner")
		}
	}
}

func TestGoConditionalGlobalReceiverDoesNotChooseFirstType(t *testing.T) {
	before := "package app\nfunc Execute() int {\n return service.Load()\n}\n"
	document, err := Analyze(goTestSnapshot("main.go", before, strings.Replace(before, "service.Load()", "service.Load() + 1", 1), 3, map[string]string{
		"receiver_linux.go":  "package app\nvar service A\n",
		"receiver_darwin.go": "package app\nvar service B\n",
		"types.go":           "package app\ntype A struct{}\ntype B struct{}\nfunc (a A) Load() int { return 1 }\nfunc (b B) Load() int { return 2 }\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if call := goTestCall(t, document, "Load"); call.Resolution == 0 {
		t.Fatal("conditional receiver selected its first declaration")
	}
}

func TestGoExternalReplacementDoesNotResolveLocalNamesake(t *testing.T) {
	before := "package app\nimport svc \"example.test/service\"\nfunc Execute() int {\n return svc.Load()\n}\n"
	snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "svc.Load()", "svc.Load() + 1", 1), 4, map[string]string{
		"service/go.mod":     "module example.test/service\n\ngo 1.27\n",
		"service/service.go": "package service\nfunc Load() int { return 1 }\n",
	})
	for index := range snapshot.Files {
		if snapshot.Files[index].NewPath == "go.mod" {
			snapshot.Files[index].Base.Content += "replace example.test/service => ../outside\n"
			snapshot.Files[index].Head.Content += "replace example.test/service => ../outside\n"
		}
	}
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if call := goTestCall(t, document, "Load"); call.Resolution == 0 || len(call.TargetIDs) != 0 {
		t.Fatalf("external replacement read a local namesake: %+v", call)
	}
	if !strings.Contains(strings.Join(document.Limitations, "\n"), "outside the pinned tree") {
		t.Fatal("external replacement has no source limitation")
	}
}

func FuzzGoSourceAnalysis(f *testing.F) {
	for _, source := range []string{"package app\nfunc F() {}\n", "package app\nfunc A() {}; func B() { A() }\n", "package app\nfunc F( {\n", "package app\ntype Reader interface { Read() int }\n"} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 32768 {
			t.Skip()
		}
		snapshot := Snapshot{Version: 1, IsComplete: true, Files: []FileSnapshot{{NewPath: "main.go", Operation: "A", Head: &SourceSnapshot{Path: "main.go", Revision: Head, Content: source}, ChangedLines: []ChangedLine{{Head, 1}}}}}
		document, err := Analyze(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateDocument(document); err != nil {
			t.Fatal(err)
		}
		again, err := Analyze(snapshot)
		if err != nil || canonicalJSON(document) != canonicalJSON(again) {
			t.Fatal("Go source analysis is not deterministic")
		}
	})
}

func BenchmarkGoPackageIndex(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			before := "package app\nfunc Execute() int {\n return helper()\n}\n"
			snapshot := goTestSnapshot("app/main.go", before, strings.Replace(before, "helper()", "helper() + 1", 1), 3, map[string]string{
				"app/helper.go": "package app\nfunc helper() int { return 1 }\n",
			})
			for index := range count {
				addMRGraphContext(&snapshot, fmt.Sprintf("other%d/other.go", index), "package other\nfunc helper() int { return 2 }\n")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := Analyze(snapshot); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
