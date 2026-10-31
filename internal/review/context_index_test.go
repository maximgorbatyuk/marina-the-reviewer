package review

import (
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func legacyContextSliceKey(s CodeSlice) string {
	return fmt.Sprintf("%s|%d|%d|%d", s.Path, s.Revision, s.StartLine, s.EndLine)
}

func legacyContextNormalize(batch Batch, d Document) Batch {
	batch = legacyContextSelectImports(batch, d)
	batch.Blocks = append([]Block{}, batch.Blocks...)
	primary := []CodeSlice{}
	for _, b := range batch.Blocks {
		for _, s := range []*CodeSlice{b.Base, b.Head} {
			if s != nil {
				primary = append(primary, *s)
			}
		}
	}
	seen := map[string]bool{}
	for i, b := range batch.Blocks {
		context := []CodeSlice{}
		for _, s := range b.StructuralContext {
			covered := false
			for _, p := range primary {
				if sliceContains(&p, s) {
					covered = true
					break
				}
			}
			key := legacyContextSliceKey(s)
			if !covered && !seen[key] {
				seen[key] = true
				context = append(context, s)
			}
		}
		b.StructuralContext = context
		batch.Blocks[i] = b
	}
	return batch
}

func legacyContextSelectImports(batch Batch, d Document) Batch {
	if d.SelectionVersion == 0 {
		return batch
	}
	isImports := func(s CodeSlice) bool {
		for _, decl := range d.DeclarationContext {
			if legacyContextSliceKey(decl) == legacyContextSliceKey(s) && (strings.HasPrefix(strings.TrimSpace(decl.Content), "package ") || strings.HasPrefix(strings.TrimSpace(decl.Content), "import ")) {
				return true
			}
		}
		return false
	}
	identifiers := map[string]bool{}
	words := regexp.MustCompile(`[A-Za-z_$][\w$]*`)
	for _, b := range batch.Blocks {
		ss := []CodeSlice{}
		if b.Base != nil {
			ss = append(ss, *b.Base)
		}
		if b.Head != nil {
			ss = append(ss, *b.Head)
		}
		for _, s := range b.StructuralContext {
			if !isImports(s) {
				ss = append(ss, s)
			}
		}
		for _, s := range ss {
			for _, word := range words.FindAllString(s.Content, -1) {
				identifiers[word] = true
			}
		}
	}
	pattern := regexp.MustCompile(`^import\s+(?:static\s+)?[\w.$]+\.([\w$]+|\*)\s*;$`)
	batch.Blocks = append([]Block{}, batch.Blocks...)
	for i, b := range batch.Blocks {
		context := []CodeSlice{}
		for _, s := range b.StructuralContext {
			if !isImports(s) {
				context = append(context, s)
				continue
			}
			ls := lines(s.Content)
			selected := []int{}
			malformed := false
			for line, text := range ls {
				text = strings.TrimSpace(text)
				m := pattern.FindStringSubmatch(text)
				changed := slices.ContainsFunc(d.Coverage.ContextLines, func(ch ChangeDisposition) bool {
					return ch.Revision == s.Revision && ch.Line == s.StartLine+line && (ch.Path == s.Path || slices.ContainsFunc(batch.Blocks, func(b Block) bool { return b.FilePath == ch.Path && b.Base != nil && b.Base.Path == s.Path }))
				})
				if changed || strings.HasPrefix(text, "package ") || m != nil && (m[1] == "*" || identifiers[m[1]]) {
					selected = append(selected, line)
				} else if strings.HasPrefix(text, "import ") && m == nil {
					malformed = true
				}
			}
			if malformed {
				context = append(context, s)
				continue
			}
			for len(selected) > 0 {
				end := 1
				for end < len(selected) && selected[end] <= selected[end-1]+3 {
					end++
				}
				first, last := selected[0], selected[end-1]
				part := s
				part.StartLine = s.StartLine + first
				part.EndLine = s.StartLine + last
				part.Content = strings.Join(ls[first:last+1], "\n")
				part.IsComplete = true
				context = append(context, part)
				selected = selected[end:]
			}
		}
		b.StructuralContext = context
		batch.Blocks[i] = b
	}
	return batch
}

func legacyContextHasBody(n GraphNode, blocks map[string]Block) bool {
	for _, b := range blocks {
		ss := append([]CodeSlice{}, b.StructuralContext...)
		s := b.Base
		if n.Revision == Head {
			s = b.Head
		}
		if s != nil {
			ss = append(ss, *s)
		}
		for _, s := range ss {
			if s.Revision == n.Revision && s.Path == n.Path && s.IsComplete && s.StartLine <= n.StartLine && s.EndLine >= n.EndLine {
				return true
			}
		}
	}
	return false
}

func contextOracleSlice(path string, revision Revision, first, last int, content string, complete bool) CodeSlice {
	return CodeSlice{Path: path, Revision: revision, StartLine: first, EndLine: last, Content: content, SymbolStartLine: first, SymbolEndLine: last, IsComplete: complete}
}
func contextOraclePointer(source CodeSlice) *CodeSlice { return &source }

func TestContextIndexLegacyNormalizationOracle(t *testing.T) {
	type sample struct {
		name     string
		batch    Batch
		document Document
	}
	header := contextOracleSlice("Old.java", Base, 1, 6, "package p;\nimport p.Used;\nimport p.Drop;\nimport p.*;\nimport static p.Constants.MAX;\nimport p.Other;", true)
	primary := contextOracleSlice("Old.java", Base, 10, 12, "Used value;\nint n = MAX;\nreturn n;", true)
	sameKey := header
	sameKey.Content = "not an import but the declaration key matches"
	malformed := header
	malformed.Content = "package p;\nimport p.Used;\nimport broken ???;\nimport p.Drop;"
	partial := contextOracleSlice("Old.java", Base, 11, 14, "partial", false)
	complete := contextOracleSlice("Other.java", Head, 1, 8, "complete", true)
	duplicate := complete
	duplicate.Content = "second body with the same key"
	samples := []sample{
		{name: "nil"},
		{name: "empty slices", batch: Batch{Blocks: []Block{}}, document: Document{DeclarationContext: []CodeSlice{}}},
		{name: "imports wildcard static and rename coverage", batch: Batch{Blocks: []Block{{ID: "rename", FilePath: "New.java", Base: &primary, StructuralContext: []CodeSlice{header}}}}, document: Document{DeclarationContext: []CodeSlice{header}, Coverage: Coverage{ContextLines: []ChangeDisposition{{Path: "New.java", Revision: Base, Line: 3}}}}},
		{name: "malformed import keeps complete slice", batch: Batch{Blocks: []Block{{ID: "malformed", Base: &primary, StructuralContext: []CodeSlice{malformed}}}}, document: Document{DeclarationContext: []CodeSlice{malformed}}},
		{name: "same source key different content", batch: Batch{Blocks: []Block{{ID: "same-key", Base: &primary, StructuralContext: []CodeSlice{sameKey}}}}, document: Document{DeclarationContext: []CodeSlice{header}}},
		{name: "duplicate declaration key first is not import", batch: Batch{Blocks: []Block{{ID: "duplicate-key", Base: &primary, StructuralContext: []CodeSlice{header}}}}, document: Document{DeclarationContext: []CodeSlice{sameKey, header}}},
		{name: "primary containment ignores completeness", batch: Batch{Blocks: []Block{{ID: "primary", Base: contextOraclePointer(contextOracleSlice("Old.java", Base, 1, 20, "partial primary", false)), StructuralContext: []CodeSlice{header, partial}}}}},
		{name: "partial overlap remains", batch: Batch{Blocks: []Block{{ID: "overlap", Base: &primary, StructuralContext: []CodeSlice{partial}}}}},
		{name: "same key preserves first content", batch: Batch{Blocks: []Block{{ID: "first", StructuralContext: []CodeSlice{complete}}, {ID: "second", StructuralContext: []CodeSlice{duplicate}}}}},
		{name: "base and head use separate keys", batch: Batch{Blocks: []Block{{ID: "revisions", StructuralContext: []CodeSlice{header, contextOracleSlice("Old.java", Head, 1, 6, header.Content, true)}}}}, document: Document{DeclarationContext: []CodeSlice{header}}},
		{name: "nil primaries preserve nonimports", batch: Batch{Blocks: []Block{{ID: "nil-primary", StructuralContext: []CodeSlice{complete, partial}}}}},
	}
	random := rand.New(rand.NewSource(20261030))
	for i := 0; i < 24; i++ {
		document := Document{DeclarationContext: []CodeSlice{}}
		batch := Batch{Blocks: []Block{}}
		for declaration := 0; declaration < 12; declaration++ {
			path := fmt.Sprintf("Fixture%d.java", declaration%4)
			rev := Revision(declaration % 2)
			text := "ordinary declaration"
			if declaration%3 == 0 {
				text = "package p;\nimport p.Used;\nimport p.Unused;"
			}
			document.DeclarationContext = append(document.DeclarationContext, contextOracleSlice(path, rev, declaration+1, declaration+3, text, declaration%2 == 0))
		}
		for block := 0; block < 6; block++ {
			source := contextOracleSlice(fmt.Sprintf("Fixture%d.java", block%4), Revision(block%2), 15, 20, "Used value;\nreturn value;", random.Intn(2) == 0)
			value := Block{ID: fmt.Sprintf("generated-%d-%d", i, block), FilePath: source.Path, StructuralContext: []CodeSlice{}}
			if random.Intn(3) != 0 {
				if source.Revision == Base {
					value.Base = &source
				} else {
					value.Head = &source
				}
			}
			for n := 0; n < 5; n++ {
				value.StructuralContext = append(value.StructuralContext, document.DeclarationContext[random.Intn(len(document.DeclarationContext))])
			}
			batch.Blocks = append(batch.Blocks, value)
		}
		samples = append(samples, sample{fmt.Sprintf("seeded-%d", i), batch, document})
	}
	for _, sample := range samples {
		for _, selection := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/selection%d", sample.name, selection), func(t *testing.T) {
				document := sample.document
				document.SelectionVersion = selection
				beforeBatch, beforeDocument := canonicalJSON(sample.batch), canonicalJSON(document)
				wantImports := legacyContextSelectImports(sample.batch, document)
				gotImports := selectImports(sample.batch, document)
				if !reflect.DeepEqual(gotImports, wantImports) {
					t.Fatalf("import selection changed\nwant: %s\ngot: %s", canonicalJSON(wantImports), canonicalJSON(gotImports))
				}
				want := legacyContextNormalize(sample.batch, document)
				got := normalizeContext(sample.batch, document)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("context normalization changed\nwant: %s\ngot: %s", canonicalJSON(want), canonicalJSON(got))
				}
				if canonicalJSON(sample.batch) != beforeBatch || canonicalJSON(document) != beforeDocument {
					t.Fatal("context selection changed its input")
				}
			})
		}
	}
}

func TestContextIndexCompleteBodyOracle(t *testing.T) {
	type sample struct {
		name   string
		node   GraphNode
		blocks map[string]Block
	}
	node := GraphNode{ID: "method", Path: "A.java", Revision: Head, StartLine: 3, EndLine: 8, Kind: 4}
	body := contextOracleSlice("A.java", Head, 1, 10, "complete", true)
	partial := body
	partial.IsComplete = false
	base := body
	base.Revision = Base
	samples := []sample{
		{name: "nil", node: node},
		{name: "head primary", node: node, blocks: map[string]Block{"b": {Head: &body}}},
		{name: "base primary is separate", node: node, blocks: map[string]Block{"b": {Base: &base}}},
		{name: "incomplete primary", node: node, blocks: map[string]Block{"b": {Head: &partial}}},
		{name: "complete structural body", node: node, blocks: map[string]Block{"b": {StructuralContext: []CodeSlice{body}}}},
		{name: "partial structural body", node: node, blocks: map[string]Block{"b": {StructuralContext: []CodeSlice{partial}}}},
		{name: "duplicate complete and partial", node: node, blocks: map[string]Block{"a": {StructuralContext: []CodeSlice{partial}}, "b": {StructuralContext: []CodeSlice{body, body}}}},
		{name: "adjacent intervals do not make a complete body", node: node, blocks: map[string]Block{"a": {Head: contextOraclePointer(contextOracleSlice("A.java", Head, 1, 5, "left", true))}, "b": {Head: contextOraclePointer(contextOracleSlice("A.java", Head, 6, 10, "right", true))}}},
		{name: "overlapping intervals do not make a complete body", node: node, blocks: map[string]Block{"a": {Head: contextOraclePointer(contextOracleSlice("A.java", Head, 1, 6, "left", true))}, "b": {Head: contextOraclePointer(contextOracleSlice("A.java", Head, 5, 10, "right", true))}}},
		{name: "same bounds other file", node: node, blocks: map[string]Block{"b": {Head: contextOraclePointer(contextOracleSlice("B.java", Head, 1, 10, "other", true))}}},
		{name: "wrong revision in head slot", node: node, blocks: map[string]Block{"b": {Head: &base}}},
		{name: "base query", node: GraphNode{ID: "base", Path: "A.java", Revision: Base, StartLine: 3, EndLine: 8}, blocks: map[string]Block{"b": {Base: &base}}},
	}
	random := rand.New(rand.NewSource(20261031))
	for i := 0; i < 48; i++ {
		blocks := map[string]Block{}
		for b := 0; b < 24; b++ {
			first := random.Intn(20) + 1
			last := first + random.Intn(10)
			source := contextOracleSlice(fmt.Sprintf("F%d.java", random.Intn(3)), Revision(random.Intn(2)), first, last, "body", random.Intn(3) != 0)
			block := Block{ID: fmt.Sprint(b)}
			switch random.Intn(3) {
			case 0:
				block.Base = &source
			case 1:
				block.Head = &source
			case 2:
				block.StructuralContext = []CodeSlice{source}
			}
			blocks[block.ID] = block
		}
		n := GraphNode{ID: fmt.Sprint(i), Path: fmt.Sprintf("F%d.java", random.Intn(3)), Revision: Revision(random.Intn(2)), StartLine: random.Intn(20) + 1}
		n.EndLine = n.StartLine + random.Intn(10)
		samples = append(samples, sample{fmt.Sprintf("seeded-%d", i), n, blocks})
	}
	for _, sample := range samples {
		t.Run(sample.name, func(t *testing.T) {
			if got, want := hasBody(sample.node, sample.blocks), legacyContextHasBody(sample.node, sample.blocks); got != want {
				t.Fatalf("complete body changed: got %t, want %t", got, want)
			}
			blocks := []Block{}
			keys := []string{}
			for key := range sample.blocks {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			for _, key := range keys {
				blocks = append(blocks, sample.blocks[key])
			}
			index := contextBodyIndex(blocks)
			if got, want := index.covers(sample.node.Path, sample.node.Revision, sample.node.StartLine, sample.node.EndLine), legacyContextHasBody(sample.node, sample.blocks); got != want {
				t.Fatalf("indexed complete body changed: got %t, want %t", got, want)
			}
		})
	}
}

func TestContextIndexImportAllocations(t *testing.T) {
	document := Document{SelectionVersion: 2, DeclarationContext: []CodeSlice{}}
	for i := 0; i < 511; i++ {
		document.DeclarationContext = append(document.DeclarationContext, contextOracleSlice(fmt.Sprintf("Unrelated%d.java", i), Head, 1, 1, "class Other {}", true))
	}
	header := contextOracleSlice("Imports.java", Head, 1, 1, "import p.Keep;", true)
	document.DeclarationContext = append(document.DeclarationContext, header)
	batch := Batch{Blocks: []Block{}}
	for i := 0; i < 128; i++ {
		primary := contextOracleSlice(fmt.Sprintf("Primary%d.java", i), Head, 10, 10, "Keep value;", true)
		batch.Blocks = append(batch.Blocks, Block{ID: fmt.Sprint(i), Head: &primary, StructuralContext: []CodeSlice{header}})
	}
	if !reflect.DeepEqual(normalizeContext(batch, document), legacyContextNormalize(batch, document)) {
		t.Fatal("allocation fixture changed semantics")
	}
	legacy := testing.AllocsPerRun(1, func() { _ = legacyContextNormalize(batch, document) })
	current := testing.AllocsPerRun(1, func() { _ = normalizeContext(batch, document) })
	t.Logf("128 blocks/512 declarations: legacy %.0f, current %.0f allocations", legacy, current)
	if legacy < 50000 {
		t.Fatalf("allocation fixture no longer exposes the old nested scan: %.0f", legacy)
	}
	if current > 5000 {
		t.Fatalf("import selection uses %.0f allocations; want at most 5000", current)
	}
}

func legacyContextContainsNode(b Block, n GraphNode) bool {
	return (slices.Contains(b.SymbolIDs, n.ID) || slices.Contains(b.SymbolIDs, n.SourceSymbolID)) && (intersects(n, b.Base) || intersects(n, b.Head))
}

func legacyContextFindBody(d Document, n GraphNode) *CodeSlice {
	sources := []CodeSlice{}
	for _, b := range d.Blocks {
		if b.Base != nil {
			sources = append(sources, *b.Base)
		}
		if b.Head != nil {
			sources = append(sources, *b.Head)
		}
	}
	sources = append(sources, d.DependencyContext...)
	for _, s := range sources {
		if s.Path != n.Path || s.Revision != n.Revision || s.StartLine > n.StartLine || s.EndLine < n.EndLine {
			continue
		}
		ls := lines(s.Content)
		first, last := n.StartLine-s.StartLine, n.EndLine-s.StartLine+1
		if first < 0 || last > len(ls) {
			continue
		}
		return &CodeSlice{n.Path, n.Revision, n.StartLine, n.EndLine, strings.Join(ls[first:last], "\n"), n.StartLine, n.EndLine, true}
	}
	return nil
}

func legacyContextDecorateDependencies(d Document, batch Batch) Batch {
	batch = legacyContextNormalize(batch, d)
	blocks := append([]Block{}, batch.Blocks...)
	nodes := map[string]GraphNode{}
	owners := map[string][]int{}
	included := map[string]bool{}
	bodyBlocks := map[string]Block{}
	for _, b := range blocks {
		bodyBlocks[b.ID] = b
	}
	for _, n := range d.Graph.Nodes {
		nodes[n.ID] = n
		for i, b := range blocks {
			if legacyContextContainsNode(b, n) {
				owners[n.ID] = append(owners[n.ID], i)
			}
		}
		if legacyContextHasBody(n, bodyBlocks) {
			included[n.ID] = true
		}
	}
	var add func(string, int)
	add = func(id string, owner int) {
		n, ok := nodes[id]
		if !ok || included[id] {
			return
		}
		body := legacyContextFindBody(d, n)
		if body == nil {
			return
		}
		blocks[owner].StructuralContext = append(append([]CodeSlice{}, blocks[owner].StructuralContext...), *body)
		included[id] = true
		if n.CounterpartID != nil {
			add(*n.CounterpartID, owner)
		}
	}
	for _, e := range d.Graph.Edges {
		if e.Kind != "call" && e.Kind != "possible-move" && e.Kind != "counterpart" {
			continue
		}
		if own := owners[e.SourceID]; len(own) > 0 {
			for _, target := range e.TargetIDs {
				add(target, own[0])
			}
		}
		for _, target := range e.TargetIDs {
			if own := owners[target]; len(own) > 0 {
				add(e.SourceID, own[0])
			}
		}
	}
	batch.Blocks = blocks
	return legacyContextNormalize(batch, d)
}

func TestContextIndexOwnersMatchLegacyNodeBlockOrder(t *testing.T) {
	random := rand.New(rand.NewSource(20261101))
	for sample := 0; sample < 64; sample++ {
		graph := Graph{Nodes: []GraphNode{}}
		for i := 0; i < 24; i++ {
			first := random.Intn(20) + 1
			id := fmt.Sprintf("node-%d", i)
			alias := fmt.Sprintf("alias-%d", i%7)
			if i%5 == 0 {
				alias = id
			}
			graph.Nodes = append(graph.Nodes, GraphNode{ID: id, SourceSymbolID: alias, Path: fmt.Sprintf("F%d.java", i%4), Revision: Revision(i % 2), StartLine: first, EndLine: first + random.Intn(10)})
		}
		graph.Nodes = append(graph.Nodes, graph.Nodes[3])
		random.Shuffle(len(graph.Nodes), func(a, b int) { graph.Nodes[a], graph.Nodes[b] = graph.Nodes[b], graph.Nodes[a] })
		blocks := []Block{}
		for i := 0; i < 32; i++ {
			node := graph.Nodes[random.Intn(len(graph.Nodes))]
			first := max(1, node.StartLine-random.Intn(3))
			source := contextOracleSlice(node.Path, node.Revision, first, node.EndLine+random.Intn(3), "source", true)
			symbols := []string{node.ID, node.SourceSymbolID, node.ID, "missing"}
			if i%3 == 0 {
				symbols = []string{node.SourceSymbolID, node.SourceSymbolID}
			}
			block := Block{ID: fmt.Sprint(i), SymbolIDs: symbols}
			if i%7 != 0 {
				if node.Revision == Head {
					block.Head = &source
				} else {
					block.Base = &source
				}
			}
			if i%9 == 0 {
				block.Head = contextOraclePointer(contextOracleSlice("Other.java", Head, 1, 2, "other", true))
			}
			blocks = append(blocks, block)
		}
		random.Shuffle(len(blocks), func(a, b int) { blocks[a], blocks[b] = blocks[b], blocks[a] })
		want := map[string][]int{}
		for _, node := range graph.Nodes {
			for i, block := range blocks {
				if legacyContextContainsNode(block, node) {
					want[node.ID] = append(want[node.ID], i)
				}
			}
		}
		if got := contextOwners(graph, blocks); !reflect.DeepEqual(got, want) {
			t.Fatalf("sample%d owner membership or order changed\nwant%v\ngot%v", sample, want, got)
		}
	}
}

func TestContextIndexReuseMatchesLegacyAcrossChangingBatches(t *testing.T) {
	imports := contextOracleSlice("Old.java", Base, 1, 5, "package p;\nimport p.First;\nimport p.Second;\nimport p.Unused;\nimport p.*;", true)
	document := Document{DeclarationContext: []CodeSlice{imports}, Coverage: Coverage{ContextLines: []ChangeDisposition{{Path: "Renamed.java", Revision: Base, Line: 4}}}}
	first := contextOracleSlice("Old.java", Base, 10, 10, "First value;", true)
	second := first
	second.Content = "Second value;"
	batches := []Batch{
		{Blocks: []Block{{ID: "first", FilePath: "Renamed.java", Base: &first, StructuralContext: []CodeSlice{imports}}}},
		{Blocks: []Block{{ID: "second", FilePath: "Old.java", Base: &second, StructuralContext: []CodeSlice{imports}}}},
		{},
		{Blocks: []Block{}},
		{Blocks: []Block{{ID: "nil-primary", StructuralContext: []CodeSlice{imports}}}},
	}
	batches = append(batches, batches[0])
	for _, selection := range []int{0, 1, 2} {
		document.SelectionVersion = selection
		index := newContextIndex(document)
		for sequence, batch := range batches {
			before := canonicalJSON(batch)
			if got, want := index.normalize(batch), legacyContextNormalize(batch, document); !reflect.DeepEqual(got, want) {
				t.Fatalf("selection%d sequence%d reused normalization changed\nwant%s\ngot%s", selection, sequence, canonicalJSON(want), canonicalJSON(got))
			}
			if canonicalJSON(batch) != before {
				t.Fatal("reused index changed its input batch")
			}
		}
	}
}

func contextOracleLines(label string) string {
	lines := []string{}
	for i := 1; i <= 48; i++ {
		lines = append(lines, fmt.Sprintf("%s line%d", label, i))
	}
	return strings.Join(lines, "\n")
}

func TestContextIndexDependencyDecorationMatchesLegacySourceOutput(t *testing.T) {
	random := rand.New(rand.NewSource(20261102))
	for sample := 0; sample < 32; sample++ {
		document := Document{Blocks: []Block{}, DependencyContext: []CodeSlice{}, DeclarationContext: []CodeSlice{}, Graph: Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}}
		for i := 0; i < 12; i++ {
			revision := Revision(i % 2)
			path := fmt.Sprintf("F%d.java", i%4)
			first := 4 + random.Intn(24)
			node := GraphNode{ID: fmt.Sprintf("node-%d", i), SourceSymbolID: fmt.Sprintf("alias-%d", i), Path: path, Revision: revision, StartLine: first, EndLine: first + 2, Kind: 4}
			if i%3 == 0 {
				counterpart := fmt.Sprintf("node-%d", (i+1)%12)
				node.CounterpartID = &counterpart
			}
			document.Graph.Nodes = append(document.Graph.Nodes, node)
			complete := contextOracleSlice(path, revision, 1, 48, contextOracleLines(fmt.Sprintf("source%d", i)), true)
			block := Block{ID: fmt.Sprintf("document-%d", i)}
			if revision == Base {
				block.Base = &complete
			} else {
				block.Head = &complete
			}
			document.Blocks = append(document.Blocks, block)
			duplicate := complete
			duplicate.Content = contextOracleLines("different content with the same key")
			document.DependencyContext = append(document.DependencyContext, duplicate)
			if i%4 == 0 {
				missing := complete
				missing.Path = "Missing.java"
				missing.Content = "short"
				document.DependencyContext = append(document.DependencyContext, missing)
			}
		}
		for i := 0; i < 20; i++ {
			source := document.Graph.Nodes[random.Intn(len(document.Graph.Nodes))]
			target := document.Graph.Nodes[random.Intn(len(document.Graph.Nodes))]
			kind := []string{"call", "possible-move", "counterpart", "contains"}[i%4]
			document.Graph.Edges = append(document.Graph.Edges, GraphEdge{ID: fmt.Sprint(i), Kind: kind, SourceID: source.ID, TargetIDs: []string{target.ID}})
		}
		batches := []Batch{{}, {Blocks: []Block{}}}
		for variant := 0; variant < 4; variant++ {
			batch := Batch{Blocks: []Block{}}
			for i := 0; i < 4; i++ {
				node := document.Graph.Nodes[(i*3+variant)%len(document.Graph.Nodes)]
				primary := contextOracleSlice(node.Path, node.Revision, node.StartLine, node.EndLine, fmt.Sprintf("primary%d\nbody\nend", i), variant%2 == 0)
				block := Block{ID: fmt.Sprintf("batch-%d-%d", variant, i), FilePath: node.Path, SymbolIDs: []string{node.ID, node.SourceSymbolID, node.ID}, StructuralContext: []CodeSlice{}}
				if node.Revision == Base {
					block.Base = &primary
				} else {
					block.Head = &primary
				}
				if i%2 == 0 {
					partial := primary
					partial.IsComplete = false
					partial.StartLine++
					block.StructuralContext = append(block.StructuralContext, partial)
				}
				batch.Blocks = append(batch.Blocks, block)
			}
			batches = append(batches, batch)
		}
		for _, selection := range []int{0, 1, 2} {
			document.SelectionVersion = selection
			index := newContextIndex(document)
			for _, node := range document.Graph.Nodes {
				if got, want := index.findBody(node), legacyContextFindBody(document, node); !reflect.DeepEqual(got, want) {
					t.Fatalf("sample%d selection%d body source selection changed", sample, selection)
				}
			}
			for variant, batch := range batches {
				beforeBatch, beforeDocument := canonicalJSON(batch), canonicalJSON(document)
				want := legacyContextDecorateDependencies(document, batch)
				if got := decorateDependencies(document, batch); !reflect.DeepEqual(got, want) {
					t.Fatalf("sample%d selection%d variant%d decoration changed\nwant%s\ngot%s", sample, selection, variant, canonicalJSON(want), canonicalJSON(got))
				}
				if got := index.decorateDependencies(batch); !reflect.DeepEqual(got, want) {
					t.Fatalf("sample%d selection%d variant%d reused decoration changed", sample, selection, variant)
				}
				if canonicalJSON(batch) != beforeBatch || canonicalJSON(document) != beforeDocument {
					t.Fatal("decoration changed source input")
				}
			}
		}
	}
}

func TestContextIndexWarmOriginalWordsAllocations(t *testing.T) {
	imports := contextOracleSlice("Imports.java", Head, 1, 1, "import p.Keep;", true)
	document := Document{SelectionVersion: 2, Blocks: []Block{}, DeclarationContext: []CodeSlice{imports}}
	for i := 0; i < 32; i++ {
		primary := contextOracleSlice(fmt.Sprintf("Primary%d.java", i), Head, 10, 140, fmt.Sprintf("Keep field%d;\n", i)+strings.Repeat("field = helper(value);\n", 128), true)
		structure := contextOracleSlice(fmt.Sprintf("Dependency%d.java", i), Head, 1, 66, fmt.Sprintf("Dependency%d reference;\n", i)+strings.Repeat("reference = extra;\n", 64), true)
		document.Blocks = append(document.Blocks, Block{ID: fmt.Sprint(i), Head: &primary, StructuralContext: []CodeSlice{structure, imports}})
	}
	batch := Batch{Blocks: document.Blocks}
	index := newContextIndex(document)
	want := legacyContextSelectImports(batch, document)
	if got := index.selectImports(batch); !reflect.DeepEqual(got, want) {
		t.Fatal("warm word fixture changed import selection")
	}
	allocations := testing.AllocsPerRun(1, func() { _ = index.selectImports(batch) })
	t.Logf("warm original32body/32structural sources: %.0f allocations", allocations)
	if allocations > 400 {
		t.Fatalf("warm original source word lookup uses %.0f allocations; want at most 400", allocations)
	}
}

func TestContextIndexWordsMatchLegacyRegexAndCacheOnlyOriginalText(t *testing.T) {
	texts := []string{"", " \n\t#@", "Used $field _private alpha9", "package p;\nimport static p.Type.MAX;", "quoted \"word\" // comment\nnext", "имя Ω 𐐀 $name _9", `escaped\u0041 name$with$dollar`}
	document := Document{Blocks: []Block{}, DependencyContext: []CodeSlice{}, DeclarationContext: []CodeSlice{contextOracleSlice("Declaration.java", Head, 1, 1, "declarationOnlyToken", true)}}
	for i, text := range texts {
		source := contextOracleSlice(fmt.Sprintf("Source%d.java", i), Revision(i%2), 1, 1, text, true)
		block := Block{ID: fmt.Sprint(i)}
		switch i % 4 {
		case 0:
			block.Base = &source
		case 1:
			block.Head = &source
		case 2:
			block.StructuralContext = []CodeSlice{source}
		case 3:
			document.DependencyContext = append(document.DependencyContext, source)
		}
		document.Blocks = append(document.Blocks, block)
	}
	index := newContextIndex(document)
	reference := regexp.MustCompile(`[A-Za-z_$][\w$]*`)
	for _, text := range texts {
		want := reference.FindAllString(text, -1)
		if got := index.wordsFor(text); !reflect.DeepEqual(got, want) {
			t.Fatalf("original word tokenization changed for %q: got%v want%v", text, got, want)
		}
		if _, ok := index.wordCache[text]; !ok {
			t.Fatalf("eligible original text was not memoized: %q", text)
		}
		size := index.wordCacheBytes
		if got := index.wordsFor(text); !reflect.DeepEqual(got, want) || index.wordCacheBytes != size {
			t.Fatal("repeated original text changed its words or cache charge")
		}
	}
	if cached, ok := index.wordCache[""]; !ok || cached != nil {
		t.Fatal("cached empty word list was not retained with its presence marker")
	}
	count, size := len(index.wordCache), index.wordCacheBytes
	dynamic := []string{"declarationOnlyToken", "unknownGeneratedPrefix1", "unknownGeneratedPrefix1 + moreTokens", texts[2] + " appendedSplitPart"}
	for _, text := range dynamic {
		if got, want := index.wordsFor(text), reference.FindAllString(text, -1); !reflect.DeepEqual(got, want) {
			t.Fatal("uncached dynamic source lost words")
		}
	}
	if len(index.wordCache) != count || index.wordCacheBytes != size {
		t.Fatal("unknown, declaration-only or growing split text was retained in the cache")
	}
}

func TestContextIndexWordMemoBudgetKeepsAllWordsWithoutRetainingOversize(t *testing.T) {
	document := Document{DependencyContext: []CodeSlice{}}
	for i := 0; i < 8; i++ {
		content := fmt.Sprintf("UniquePrefix%d ", i) + strings.Repeat("keptWord ", 65536)
		document.DependencyContext = append(document.DependencyContext, contextOracleSlice(fmt.Sprintf("Large%d.java", i), Head, 1, 1, content, true))
	}
	index := newContextIndex(document)
	reference := regexp.MustCompile(`[A-Za-z_$][\w$]*`)
	uncached := 0
	for _, source := range document.DependencyContext {
		got := index.wordsFor(source.Content)
		want := reference.FindAllString(source.Content, -1)
		if !reflect.DeepEqual(got, want) || len(got) != 65537 {
			t.Fatal("memo admission removed or changed source words")
		}
		if index.wordCacheBytes > contextWordCacheMaximumBytes {
			t.Fatalf("word memo charge exceeds its limit: %d", index.wordCacheBytes)
		}
		if _, ok := index.wordCache[source.Content]; !ok {
			uncached++
		}
	}
	if uncached == 0 || len(index.wordCache) == 0 {
		t.Fatal("bounded memo fixture did not exercise both retained and uncached original text")
	}
	beforeCount, beforeBytes := len(index.wordCache), index.wordCacheBytes
	for _, source := range document.DependencyContext {
		if _, ok := index.wordCache[source.Content]; !ok {
			if got, want := index.wordsFor(source.Content), reference.FindAllString(source.Content, -1); !reflect.DeepEqual(got, want) {
				t.Fatal("uncached original source changed on repeat")
			}
			break
		}
	}
	if len(index.wordCache) != beforeCount || index.wordCacheBytes != beforeBytes {
		t.Fatal("repeated over-budget text changed cache admission")
	}
	largeWordCount := contextWordCacheMaximumBytes/16 + 1
	largeContent := strings.Repeat("largeWord ", largeWordCount)
	largeIndex := newContextIndex(Document{DependencyContext: []CodeSlice{contextOracleSlice("Oversize.java", Head, 1, 1, largeContent, true)}})
	largeWords := largeIndex.wordsFor(largeContent)
	if len(largeWords) != largeWordCount || len(largeIndex.wordCache) != 0 || largeIndex.wordCacheBytes != 0 {
		t.Fatal("an oversized original word list was truncated or memoized")
	}
	for _, word := range largeWords {
		if word != "largeWord" {
			t.Fatal("oversized original source changed a token")
		}
	}
	small := newContextIndex(Document{Blocks: []Block{{Head: contextOraclePointer(contextOracleSlice("A.java", Head, 1, 1, "oneToken", true))}}})
	small.wordCacheBytes = contextWordCacheMaximumBytes
	if got := small.wordsFor("oneToken"); !reflect.DeepEqual(got, []string{"oneToken"}) || len(small.wordCache) != 0 || small.wordCacheBytes != contextWordCacheMaximumBytes {
		t.Fatal("full memo budget blocked computation or retained more text")
	}
}
