package review

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func contextTrialFixture() Document {
	d := Document{SelectionVersion: 2}
	for i := 0; i < 32; i++ {
		path := fmt.Sprintf("new/File%d.java", i)
		imports := contextOracleSlice(path, Head, 1, 5, "package p;\nimport p.Keep;\n\nimport p.Other;\nimport p.*;", true)
		d.DeclarationContext = append(d.DeclarationContext, imports)
		body := contextOracleSlice(path, Head, 10, 11, "Keep value;\nOther second;", i%2 == 0)
		if i%3 == 0 {
			body.Path = fmt.Sprintf("old/File%d.java", i)
		}
		b := Block{ID: fmt.Sprintf("b%d", i), FilePath: path, Head: &body, StructuralContext: []CodeSlice{imports, imports}}
		if i%3 == 0 {
			base := body
			base.Revision = Base
			b.Base = &base
			d.Coverage.ContextLines = append(d.Coverage.ContextLines, ChangeDisposition{Path: path, Revision: Head, Line: 4})
		}
		if i > 0 {
			b.StructuralContext = append(b.StructuralContext, *d.Blocks[i-1].Head)
		}
		d.Blocks = append(d.Blocks, b)
	}
	return d
}

func TestContextTrialMatchesLegacyAcrossGrowingAndResetBatches(t *testing.T) {
	for _, selection := range []int{0, 1, 2} {
		d := contextTrialFixture()
		d.SelectionVersion = selection
		index := newContextIndex(d)
		before := defaultJSON(d)
		for _, count := range []int{0, 1, 5, 32, 2, 17, 0, 32, 1} {
			batch := Batch{Index: 1, Count: 1, Blocks: d.Blocks[:count]}
			want := legacyContextNormalize(batch, d)
			got := index.normalizeTrial(batch)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("selection %d, blocks %d: trial normalization changed", selection, count)
			}
		}
		if defaultJSON(d) != before {
			t.Fatal("trial normalization changed the source document")
		}
	}
}

func TestContextTrialReusesBatchStorage(t *testing.T) {
	d := contextTrialFixture()
	index := newContextIndex(d)
	batch := Batch{Blocks: d.Blocks}
	index.normalizeTrial(batch)
	current := testing.AllocsPerRun(2, func() { _ = index.normalize(batch) })
	trial := testing.AllocsPerRun(2, func() { _ = index.normalizeTrial(batch) })
	t.Logf("normal normalization %.0f allocations, reusable trial %.0f", current, trial)
	if trial >= current/2 {
		t.Fatalf("trial normalization retains per-block allocations: %.0f vs %.0f", trial, current)
	}
}

func TestContextTrialKeepsLegacyImportAndOverlapDecisions(t *testing.T) {
	for _, content := range []string{
		"package p;\r\nimport p.Keep;\r\n\r\nimport p.Unused;\r\nimport p.*;\r\n",
		"package p;\nimport broken ???;\nimport p.Keep;",
		"package p;\nimport p.Keep;\n\n\n\nimport p.Other;",
		"package p;\nimport static p.Type.Keep;\n// non import text",
	} {
		for _, selection := range []int{0, 1, 2} {
			d := contextTrialFixture()
			d.SelectionVersion = selection
			for i := range d.Blocks {
				d.Blocks[i].StructuralContext[0].Content = content
				d.DeclarationContext[i].Content = "package declarationDifferent;"
			}
			index := newContextIndex(d)
			for _, count := range []int{1, 4, 32, 2, 0, 32} {
				batch := Batch{Blocks: d.Blocks[:count]}
				if !reflect.DeepEqual(index.normalizeTrial(batch), legacyContextNormalize(batch, d)) {
					t.Fatalf("selection %d, blocks %d, import %q", selection, count, content)
				}
			}
		}
	}
}

func TestContextTrialImportCacheDoesNotRetainTransientOrOversizeText(t *testing.T) {
	large := strings.Repeat("package p;\n", 100000)
	d := Document{Blocks: []Block{{Head: &CodeSlice{Content: large}}}}
	index := newContextIndex(d)
	for i := 0; i < 256; i++ {
		index.importText(fmt.Sprintf("package dynamic%d;\nimport p.Keep;", i))
	}
	if len(index.importCache) != 0 || index.importCacheBytes != 0 {
		t.Fatal("transient import text was retained")
	}
	parsed := index.importText(large)
	if len(parsed.items) != len(lines(large)) || parsed.full != strings.Join(lines(large), "\n") {
		t.Fatal("oversize import text was truncated")
	}
	if len(index.importCache) != 0 || index.importCacheBytes > contextImportCacheMaximumBytes {
		t.Fatal("oversize import text was retained")
	}
}

func TestContextTrialDoesNotExpandAChangedMalformedImport(t *testing.T) {
	for _, renamed := range []bool{false, true} {
		source := CodeSlice{Path: "old/A.java", Revision: Head, StartLine: 1, EndLine: 2,
			Content: "import broken\nimport p.Unused;", IsComplete: true}
		body := CodeSlice{Path: "old/A.java", Revision: Head, StartLine: 5, EndLine: 5, Content: "return;"}
		path := source.Path
		block := Block{ID: "b", FilePath: path, Head: &body, StructuralContext: []CodeSlice{source}}
		if renamed {
			path, block.FilePath = "new/A.java", "new/A.java"
			base := body
			base.Revision = Base
			block.Base = &base
		}
		d := Document{SelectionVersion: 2, Blocks: []Block{block}, DeclarationContext: []CodeSlice{source},
			Coverage: Coverage{ContextLines: []ChangeDisposition{{Path: path, Revision: Head, Line: 1}}}}
		batch := Batch{Blocks: d.Blocks}
		if got, want := newContextIndex(d).normalizeTrial(batch), legacyContextNormalize(batch, d); !reflect.DeepEqual(got, want) {
			t.Fatalf("changed malformed import expanded source; renamed=%t", renamed)
		}
	}
}
