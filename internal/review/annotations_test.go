package review

import (
	"strings"
	"testing"
)

func TestJavaNamedAnnotationsKeepDeclarationIdentityAndCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		methods      int
	}{
		{"fields", "class A {\n @Label(value = \"one\")\n private int first;\n @Label(value = \"two\")\n private int second;\n}\n", 0},
		{"class-methods", "class A {\n @Mark(value = \"one\")\n public int first() { return 1; }\n @Mark(value = \"two\")\n public int second() { return 2; }\n}\n", 2},
		{"interface-methods", "interface A {\n @Endpoint(path = \"/one\")\n int first();\n @Endpoint(path = \"/two\")\n int second();\n}\n", 2},
		{"nested-arguments", "class A {\n @pkg.Mark(values = {@Inner(key = \"x=y\"), @Inner(key = \"z\")})\n public int first() { return 1; }\n @pkg.Mark(values = {@Inner(key = \"z\"), @Inner(key = \"x=y\")})\n public int second() { return 2; }\n}\n", 2},
		{"field-initializers", "class A {\n @Mark(value = \"one\")\n private int first = Factory.make();\n @Mark(value = \"two\")\n private int second = Factory.make();\n}\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changes := []ChangedLine{}
			for index := range strings.Split(strings.TrimSuffix(tc.source, "\n"), "\n") {
				changes = append(changes, ChangedLine{Head, index + 1})
			}
			snapshot := Snapshot{IsComplete: true, Files: []FileSnapshot{{OldPath: "A.java", NewPath: "A.java", Operation: "A",
				Head: &SourceSnapshot{Path: "A.java", Revision: Head, Content: tc.source}, ChangedLines: changes}}}
			document, err := Analyze(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if !document.Coverage.IsComplete || len(document.Coverage.Exceptions) != 0 || document.Coverage.CoveredChangedLines+len(document.Coverage.ContextLines)+len(document.Coverage.ExcludedLines) != len(changes) {
				t.Fatalf("annotation damaged coverage: %+v", document.Coverage)
			}
			methods := 0
			for _, node := range document.Graph.Nodes {
				if node.Kind == 4 {
					methods++
				}
			}
			if methods != tc.methods {
				t.Fatalf("method declarations: got %d, want %d", methods, tc.methods)
			}
			for _, block := range document.Blocks {
				if block.Kind == "method" || block.Kind == "field" {
					if block.Head == nil || !strings.Contains(block.Head.Content, "@") {
						t.Fatal("declaration source lost its annotation")
					}
				}
			}
		})
	}
}

func TestJavaAnnotationFixKeepsTrueAmbiguityAndMissingSourceIncomplete(t *testing.T) {
	source := "class A {\n @Mark(value = \"one\")\n int repeated;\n @Mark(value = \"two\")\n int repeated;\n}\n"
	file := FileSnapshot{OldPath: "A.java", NewPath: "A.java", Operation: "A",
		Head: &SourceSnapshot{Path: "A.java", Revision: Head, Content: source}, ChangedLines: []ChangedLine{{Head, 3}, {Head, 5}}}
	document, err := Analyze(Snapshot{IsComplete: true, Files: []FileSnapshot{file}})
	if err != nil || document.Coverage.IsComplete || len(document.Coverage.Exceptions) == 0 {
		t.Fatal("duplicate field declarations were accepted as complete")
	}
	file.Head.Content = "class A {\n @Mark(value = \"one\")\n int first;\n}\n"
	file.ChangedLines = []ChangedLine{{Head, 3}}
	document, err = Analyze(Snapshot{IsComplete: false, Files: []FileSnapshot{file}})
	if err != nil || document.Coverage.IsComplete {
		t.Fatal("incomplete source snapshot was accepted as complete")
	}
}
