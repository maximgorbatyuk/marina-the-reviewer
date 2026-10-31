package review

import (
	"fmt"
	"strings"
	"testing"
)

func TestGoChangedLineIndexPreservesSourceOrderAndBoundsWork(t *testing.T) {
	changes := []ChangedLine{}
	for line := 1; line <= 10000; line++ {
		changes = append(changes, ChangedLine{Head, line}, ChangedLine{Base, line})
	}
	index, err := newGoChangedLineIndex(t.Context(), changes)
	if err != nil {
		t.Fatal(err)
	}
	for line := 1; line <= 10000; line++ {
		before := &CodeSlice{Revision: Base, StartLine: line, EndLine: line}
		after := &CodeSlice{Revision: Head, StartLine: line, EndLine: line}
		found := index.within(before, after)
		if len(found) != 2 || found[0] != (ChangedLine{Head, line}) || found[1] != (ChangedLine{Base, line}) {
			t.Fatalf("changed-line order or ownership changed at %d: %v", line, found)
		}
	}
	if index.visited != len(changes) {
		t.Fatalf("Go unit selection rescanned unrelated changes: %d", index.visited)
	}
}

func TestGoDeclarationRangesSkipUnrelatedFunctions(t *testing.T) {
	file := &goSourceFile{}
	for line := 1; line <= 10000; line++ {
		file.declarations = append(file.declarations, &goDeclaration{source: CodeSlice{StartLine: line * 3, EndLine: line*3 + 1}})
	}
	file.indexDeclarationRanges()
	for index := range file.declarations {
		line := (index + 1) * 3
		found := file.overlappingDeclarations(line, line)
		if len(found) != 1 || found[0] != file.declarations[index] {
			t.Fatalf("wrong declaration interval: %d", line)
		}
	}
	if file.rangeVisited != len(file.declarations) {
		t.Fatalf("Go context rescanned unrelated declarations: %d", file.rangeVisited)
	}
}

func TestGoLargeAddedFileKeepsEveryFunctionAndBlankLine(t *testing.T) {
	var source strings.Builder
	source.WriteString("package app\n\n")
	for index := range 1000 {
		fmt.Fprintf(&source, "func F%d() int { return %d }\n\n", index, index)
	}
	changes := []ChangedLine{}
	for line := range strings.Count(source.String(), "\n") {
		changes = append(changes, ChangedLine{Head, line + 1})
	}
	snapshot := Snapshot{IsComplete: true, Files: []FileSnapshot{{NewPath: "main.go", Operation: "A", Head: &SourceSnapshot{Path: "main.go", Revision: Head, Content: source.String()}, ChangedLines: changes}}}
	document, err := Analyze(snapshot)
	if err != nil || !document.Coverage.IsComplete || len(document.Blocks) != 1000 {
		t.Fatalf("large Go file lost function or blank-line coverage: blocks=%d coverage=%+v err=%v", len(document.Blocks), document.Coverage, err)
	}
	if err := ValidateDocument(document); err != nil {
		t.Fatal(err)
	}
}
