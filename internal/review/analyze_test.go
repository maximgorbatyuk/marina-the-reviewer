package review

import (
	"strings"
	"testing"
)

func TestJavaCallsResolveReceiverTypesAndOverloadArity(t *testing.T) {
	source := "class A {\n private B service;\n int f() {\n  return service.g(1);\n }\n}\nclass B {\n int g() { return 0; }\n int g(int x) { return x; }\n}\n"
	s := Snapshot{IsComplete: true, Files: []FileSnapshot{{OldPath: "A.java", NewPath: "A.java", Operation: "A", Head: &SourceSnapshot{Path: "A.java", Revision: Head, Content: source}, ChangedLines: []ChangedLine{{Head, 4}}}}}
	d, e := Analyze(s)
	if e != nil {
		t.Fatal(e)
	}
	for _, edge := range d.Graph.Edges {
		if edge.Kind == "call" && edge.InvokedName != nil && *edge.InvokedName == "g" {
			if edge.Resolution != 0 || len(edge.TargetIDs) != 1 || edge.ReceiverType == nil || *edge.ReceiverType != "B" {
				t.Fatalf("receiver call is unresolved: %+v", edge)
			}
			return
		}
	}
	t.Fatal("call edge missing")
}
func TestAnalyzeNonJavaFallbackAndJavaUnicodeCoverage(t *testing.T) {
	for _, tc := range []struct {
		path, content string
		hunks         []Hunk
		kind          string
		complete      bool
	}{{"a.go", strings.Repeat("x\n", 250), []Hunk{{OldStart: 0, OldCount: 0, NewStart: 10, NewCount: 2, Lines: []string{"+x", "+x"}}}, "diff-region", false}, {"A.java", "class A {\n int \\u0061 = 1;\n}\n", nil, "file", true}} {
		t.Run(tc.path, func(t *testing.T) {
			s := Snapshot{IsComplete: true, Files: []FileSnapshot{{NewPath: tc.path, Operation: "A", Head: &SourceSnapshot{Path: tc.path, Revision: Head, Content: tc.content}, ChangedLines: []ChangedLine{{Head, 10}}, Hunks: tc.hunks}}}
			if tc.path == "A.java" {
				s.Files[0].ChangedLines = []ChangedLine{{Head, 2}}
			}
			d, e := Analyze(s)
			if e != nil {
				t.Fatal(e)
			}
			if len(d.Blocks) != 1 || d.Blocks[0].Kind != tc.kind || d.Blocks[0].Head.IsComplete != tc.complete || !d.Coverage.IsComplete {
				t.Fatalf("fallback lost coverage: %+v", d)
			}
		})
	}
}
func TestAnalyzeSignatureChangeKeepsCounterpart(t *testing.T) {
	before := "class A {\n int f(int value) {\n return value;\n }\n}\n"
	after := strings.Replace(before, "int value", "long value", 1)
	s := Snapshot{IsComplete: true, Files: []FileSnapshot{{OldPath: "A.java", NewPath: "A.java", Operation: "M", Base: &SourceSnapshot{Path: "A.java", Revision: Base, Content: before}, Head: &SourceSnapshot{Path: "A.java", Revision: Head, Content: after}, ChangedLines: []ChangedLine{{Base, 2}, {Head, 2}}}}}
	d, e := Analyze(s)
	if e != nil {
		t.Fatal(e)
	}
	if len(d.Blocks) != 1 || d.Blocks[0].Base == nil || d.Blocks[0].Head == nil {
		t.Fatalf("signature change was split into deletion/addition: %+v", d.Blocks)
	}
}
func TestJavaDeclarationExistenceDoesNotFollowContainingFile(t *testing.T) {
	before := "class A {\n int oldMethod() { return 1; }\n}\n"
	after := "class A {\n int newMethod() { return 2; }\n}\n"
	d, e := Analyze(Snapshot{IsComplete: true, Files: []FileSnapshot{{OldPath: "A.java", NewPath: "A.java", Operation: "M", Base: &SourceSnapshot{Path: "A.java", Revision: Base, Content: before}, Head: &SourceSnapshot{Path: "A.java", Revision: Head, Content: after}, ChangedLines: []ChangedLine{{Base, 2}, {Head, 2}}}}})
	if e != nil {
		t.Fatal(e)
	}
	if len(d.Blocks) != 2 {
		t.Fatalf("expected added and deleted declaration blocks, got %d", len(d.Blocks))
	}
	for _, b := range d.Blocks {
		if b.BaseExists != (b.Base != nil) || b.HeadExists != (b.Head != nil) {
			t.Fatal("method existence was inferred from the containing file")
		}
	}
}
