package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInputSizerMatchesSerializedWire(t *testing.T) {
	f := loadCompatibility(t)
	for _, c := range f.WireCases {
		t.Run(c.Name, func(t *testing.T) {
			o := normalizeOptions(PlanOptions{})
			d := Document{Graph: c.Input.Graph}
			s := newInputSizer(d, o)
			for _, task := range []json.RawMessage{json.RawMessage(`null`), json.RawMessage(`{"text":"Ж😀<>&\n"}`)} {
				input := c.Input
				input.Task = task
				got := s.count(input)
				want := CountTokens(o.SystemPrompt, SerializeInput(input), ResponseSchema())
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("token count differs: got %+v want %+v", got, want)
				}
			}
		})
	}
}

func TestInputSizerReusesChangedNodeCatalogue(t *testing.T) {
	input := sampleBatch().Input
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("n%d", i)
		input.Graph.Nodes = append(input.Graph.Nodes, GraphNode{ID: id, SourceSymbolID: id, Path: "A.java", Name: id, Signature: "int " + id + "()", Kind: 4, Revision: Head, Change: 2, StartLine: i + 1, EndLine: i + 1})
	}
	input.Graph.Sha256 = strings.Repeat("c", 64)
	o := normalizeOptions(PlanOptions{})
	s := newInputSizer(Document{Graph: input.Graph}, o)
	for i := 0; i < 64; i++ {
		b := input.Batch.Blocks[0]
		b.SymbolIDs = []string{input.Graph.Nodes[i].ID}
		b.Base = nil
		b.Head = &CodeSlice{Path: "A.java", Revision: Head, StartLine: i + 1, EndLine: i + 1, Content: "return Ж😀;", IsComplete: true}
		input.Batch.Blocks = []Block{b}
		got := s.count(input)
		want := CountTokens(o.SystemPrompt, SerializeInput(input), ResponseSchema())
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("probe %d differs: got %+v want %+v", i, got, want)
		}
	}
	if s.nodeCatalogueBuilds != 1 {
		t.Fatalf("rebuilt immutable changed nodes %d times", s.nodeCatalogueBuilds)
	}
}

func TestPlanMatchesSerializationOracle(t *testing.T) {
	f := loadCompatibility(t)
	for _, c := range f.AnalysisCases {
		for _, provider := range []string{"", "mimo", "deepseek"} {
			for _, length := range []int{10000, 18000, 48000, 131072} {
				t.Run(fmt.Sprintf("%s/%s/%d", c.Name, provider, length), func(t *testing.T) {
					o := normalizeOptions(PlanOptions{ContextLength: length, OutputTokens: 1024, Provider: provider})
					got, ge := plan(c.Document, o, 0, false)
					want, we := planSerializationOracle(c.Document, o, 0, false)
					if fmt.Sprint(ge) != fmt.Sprint(we) {
						t.Fatalf("error differs: %v / %v", ge, we)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatal("prepared plan differs from serialization oracle")
					}
				})
			}
		}
	}
}

func TestIndexedProjectionAndSizingMatchEveryWireCase(t *testing.T) {
	graph := Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Limitations: []string{"Ж😀", "quote\"\\\n"}, Sha256: strings.Repeat("f", 64)}
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("n%d", i)
		other := fmt.Sprintf("n%d", i^1)
		owner := fmt.Sprintf("C%d", i/12)
		graph.Nodes = append(graph.Nodes, GraphNode{ID: id, SourceSymbolID: "source-" + id, Path: fmt.Sprintf("src/Ж/C%d.java", i/12), DeclaringType: &owner, Name: fmt.Sprintf("f%d", i/2), Signature: fmt.Sprintf("String f%d(Ж😀)", i/2), Kind: 4, Revision: Revision(i % 2), Change: (i / 2) % 4, StartLine: (i%12)/2*5 + 1, EndLine: (i%12)/2*5 + 4, CounterpartID: &other})
	}
	for i := range graph.Nodes {
		if i%12 < 2 {
			graph.Nodes[i].Kind = 0
			graph.Nodes[i].Name = fmt.Sprintf("C%d", i/12)
			graph.Nodes[i].StartLine = 1
			graph.Nodes[i].EndLine = 40
		}
	}
	for i := 0; i < 120; i++ {
		line := i%30 + 1
		name := fmt.Sprintf("f%d", (i+7)%30)
		owner := fmt.Sprintf("C%d", (i+7)%5)
		kind := []string{"call", "contains", "counterpart", "possible-move"}[i%4]
		targets := []string{graph.Nodes[(i+7)%60].ID}
		if i%7 == 0 {
			targets = nil
		}
		if i%11 == 0 {
			targets = []string{}
		}
		graph.Edges = append(graph.Edges, GraphEdge{ID: fmt.Sprintf("e%d", i), Kind: kind, SourceID: graph.Nodes[i%60].ID, TargetIDs: targets, Revision: Revision(i % 2), Line: &line, InvokedName: &name, ReceiverType: &owner, Resolution: i % 3, Change: []string{"unchanged", "added", "removed", "structural", "matched-declaration", "unproven"}[i%6], Reasons: []string{"Ж😀"}})
	}
	o := normalizeOptions(PlanOptions{SystemPrompt: "Ж😀<>&\n"})
	s := newInputSizer(Document{Graph: graph}, o)
	for probe := 0; probe < 128; probe++ {
		blocks := []Block{}
		for i := 0; i < 1+probe%8; i++ {
			n := graph.Nodes[(probe*7+i*11)%len(graph.Nodes)]
			start := n.StartLine + probe%3
			end := n.EndLine
			if start > end {
				start = end
			}
			source := &CodeSlice{Path: n.Path, Revision: n.Revision, StartLine: start, EndLine: end, Content: "return \"Ж😀<>&\\n\";\u2028\u2029", IsComplete: probe%3 == 0}
			b := Block{ID: fmt.Sprintf("b%d-%d", probe, i), FilePath: n.Path, Kind: "method", SymbolIDs: []string{n.ID, "source-" + n.ID}, PartIndex: probe + 1, PartCount: probe + 2, ChangedLines: []ChangedLine{{n.Revision, start}}, Limitations: []string{}}
			if n.Revision == Base {
				b.Base = source
				b.BaseExists = true
			} else {
				b.Head = source
				b.HeadExists = true
			}
			if probe%3 == 0 {
				b.StructuralContext = []CodeSlice{*source}
			}
			blocks = append(blocks, b)
		}
		batch := Batch{Index: probe + 1, Count: probe + 2, Blocks: blocks, FileParts: []FilePart{{Path: "Ж.java", Index: probe + 1, Count: probe + 2}}, IsWholeReview: probe%2 == 0}
		actual := s.projector.project(batch)
		expected := ProjectGraph(graph, batch)
		expected.Sha256 = ""
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("projection %d differs", probe)
		}
		input := BatchInput{Version: 3, Graph: graph, Batch: batch, Limitations: []string{"Ж😀", "outside"}, Task: json.RawMessage(`{"x":"Ж😀<>&\n"}`)}
		got := s.count(input)
		want := CountTokens(o.SystemPrompt, SerializeInput(input), ResponseSchema())
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tokens %d differ: %+v / %+v", probe, got, want)
		}
	}
}

func TestDensePlanRetainsGreedySplitAndBudgetDecisions(t *testing.T) {
	snapshot := Snapshot{Version: 1, IsComplete: true, Files: []FileSnapshot{}}
	for file := 0; file < 4; file++ {
		source := "class C {\n"
		changes := []ChangedLine{}
		for method := 0; method < 6; method++ {
			source += fmt.Sprintf(" int f%d() {\n", method)
			for line := 0; line < 8; line++ {
				source += "  String text = \"Ж😀<>&\\n\";\n"
			}
			source += "  return 1;\n }\n"
			changes = append(changes, ChangedLine{Head, method*11 + 11})
		}
		source += "}\n"
		path := fmt.Sprintf("C%d.java", file)
		snapshot.Files = append(snapshot.Files, FileSnapshot{NewPath: path, Operation: "A", Head: &SourceSnapshot{Path: path, Revision: Head, Content: source}, ChangedLines: changes})
	}
	d, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, context := range []int{8000, 12000, 20000, 48000} {
		for _, output := range []int{0, 1024} {
			for _, force := range []bool{false, true} {
				o := normalizeOptions(PlanOptions{ContextLength: context, OutputTokens: output, SafetyTokens: 100, Task: json.RawMessage(`{"title":"Ж😀"}`)})
				got, ge := plan(d, o, context/2, force)
				want, we := planSerializationOracle(d, o, context/2, force)
				if fmt.Sprint(ge) != fmt.Sprint(we) {
					t.Fatalf("context %d output %d force %v errors differ: %v / %v", context, output, force, ge, we)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("context %d output %d force %v prepared plan differs", context, output, force)
				}
			}
		}
	}
}

func TestOfflinePlanPreservesSavedPrepared(t *testing.T) {
	paths := os.Getenv("MARINADOREVIEW_PLAN_ORACLE")
	if paths == "" {
		t.Skip("Set MARINADOREVIEW_PLAN_ORACLE to saved native plans.")
	}
	for _, path := range filepath.SplitList(paths) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			bytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Document Document     `json:"document"`
				Prepared PreparedPlan `json:"prepared"`
				Route    struct {
					Provider string `json:"provider"`
				} `json:"route"`
			}
			if err = json.Unmarshal(bytes, &saved); err != nil {
				t.Fatal(err)
			}
			if len(saved.Prepared.Batches) == 0 {
				t.Fatal("The oracle has no prepared batches.")
			}
			batch := saved.Prepared.Batches[0]
			current, err := Plan(saved.Document, PlanOptions{ContextLength: batch.Budget.ContextLength, SystemPrompt: batch.SystemPrompt, SafetyTokens: batch.Budget.FixedOverheadTokens, Provider: saved.Route.Provider, Task: batch.Input.Task})
			if err != nil {
				t.Fatal(err)
			}
			actual, expected := canonicalJSON(current), canonicalJSON(saved.Prepared)
			if actual != expected {
				t.Fatalf("saved prepared plan differs: current %s / expected %s", Hash(actual), Hash(expected))
			}
			t.Logf("Exact saved plan retained: %d batches, SHA256 %s", len(current.Batches), Hash(actual))
		})
	}
}

func TestInputSizerBoundsSourceCacheRetention(t *testing.T) {
	input := sampleBatch().Input
	s := newInputSizer(Document{Graph: input.Graph}, normalizeOptions(PlanOptions{}))
	for i := 1; i <= 160; i++ {
		b := input.Batch.Blocks[0]
		source := CodeSlice{Path: "imports.java", Revision: Head, StartLine: 1, EndLine: i, Content: strings.Repeat("import example.Dependency;\n", i*80), IsComplete: true}
		b.StructuralContext = []CodeSlice{source}
		b.Head = &source
		b.Base = nil
		s.blockWeight(b)
		if s.sliceCacheBytes > maximumSliceCacheBytes || s.blockCacheBytes > maximumBlockCacheBytes {
			t.Fatal("source cache exceeded its retained byte bound")
		}
	}
	if len(s.sliceWeights) > maximumSliceCacheEntries || s.blockCacheEntries > maximumBlockCacheEntries {
		t.Fatal("source cache exceeded its entry bound")
	}
	b := input.Batch.Blocks[0]
	source := CodeSlice{Path: "large.java", Revision: Head, StartLine: 1, EndLine: 1, Content: strings.Repeat("x", maximumBlockCacheBytes+1), IsComplete: true}
	b.Head = &source
	b.Base = nil
	b.StructuralContext = []CodeSlice{source}
	got := s.blockWeight(b)
	want := jsonWeight(makeWireBlock(b, s.all, s.aliases))
	if got != want {
		t.Fatalf("uncached large source weight differs: %d/%d", got, want)
	}
	if s.sliceCacheBytes > maximumSliceCacheBytes || s.blockCacheBytes > maximumBlockCacheBytes {
		t.Fatal("large source exceeded the cache bound")
	}
}

func TestInputSizerDoesNotCopyMixedGraphPayloadOnCacheHits(t *testing.T) {
	input := sampleBatch().Input
	for i := 0; i < 128; i++ {
		id := fmt.Sprintf("mixed%d", i)
		change := 0
		if i < 64 {
			change = 2
		}
		input.Graph.Nodes = append(input.Graph.Nodes, GraphNode{ID: id, SourceSymbolID: "source-" + id, Path: "A.java", Name: id, Signature: "int " + id + "()", Kind: 4, Revision: Head, Change: change, StartLine: i + 1, EndLine: i + 1})
	}
	line := 2
	input.Graph.Edges = []GraphEdge{{ID: "incoming", Kind: "call", SourceID: "mixed64", TargetIDs: []string{"mixed1"}, Line: &line, Revision: Head}, {ID: "dependency", Kind: "call", SourceID: "mixed1", TargetIDs: []string{"mixed65"}, Line: &line, Revision: Head}}
	input.Graph.Sha256 = strings.Repeat("c", 64)
	b := input.Batch.Blocks[0]
	b.SymbolIDs = []string{"mixed1"}
	b.Base = nil
	b.Head = &CodeSlice{Path: "A.java", Revision: Head, StartLine: 2, EndLine: 2, Content: "return Ж😀;", IsComplete: true}
	input.Batch.Blocks = []Block{b}
	o := normalizeOptions(PlanOptions{})
	s := newInputSizer(Document{Graph: input.Graph}, o)
	for i := 0; i < 64; i++ {
		got := s.count(input)
		want := CountTokens(o.SystemPrompt, SerializeInput(input), ResponseSchema())
		if !reflect.DeepEqual(got, want) {
			t.Fatal("mixed projection count differs")
		}
	}
	if s.projector.nodeMaterializations != 0 {
		t.Fatalf("copied mixed graph nodes %d times", s.projector.nodeMaterializations)
	}
	if s.projector.edgeMaterializations != 0 {
		t.Fatalf("copied mixed graph edges %d times", s.projector.edgeMaterializations)
	}
}

func TestInputSizerCountsMovingCounterpartsAndChangingRanks(t *testing.T) {
	input := sampleBatch().Input
	input.Graph = Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Limitations: []string{}, Sha256: strings.Repeat("e", 64)}
	blocks := []Block{}
	for i := 0; i < 48; i++ {
		baseID, headID := fmt.Sprintf("before%d", i), fmt.Sprintf("after%d", i)
		basePath, headPath := fmt.Sprintf("src/old/Ж%02d/A.java", i%17), fmt.Sprintf("src/new/Q%02d/B.java", i%19)
		beforeOwner, afterOwner := fmt.Sprintf("OldOwner%02d", i%19), fmt.Sprintf("NewOwner%02d", i%23)
		var beforeDeclaring, afterDeclaring *string
		if i%7 != 0 {
			beforeDeclaring = &beforeOwner
		}
		if i%5 != 0 {
			afterDeclaring = &afterOwner
		}
		input.Graph.Nodes = append(input.Graph.Nodes, GraphNode{ID: baseID, SourceSymbolID: baseID, Path: basePath, DeclaringType: beforeDeclaring, Name: "f", Signature: "int f(Ж😀)", Kind: i % 5, Revision: Base, Change: i % 4, StartLine: 1, EndLine: 2, CounterpartID: &headID}, GraphNode{ID: headID, SourceSymbolID: headID, Path: headPath, DeclaringType: afterDeclaring, Name: "f", Signature: "long f(Ж😀)", Kind: (i + 1) % 5, Revision: Head, Change: i % 4, StartLine: 1, EndLine: 2, CounterpartID: &baseID})
		blocks = append(blocks, Block{ID: fmt.Sprintf("moving%d", i), FilePath: headPath, Kind: "method", SymbolIDs: []string{baseID, headID}, Base: &CodeSlice{Path: basePath, Revision: Base, StartLine: 1, EndLine: 2, Content: "before\n}", IsComplete: true}, Head: &CodeSlice{Path: headPath, Revision: Head, StartLine: 1, EndLine: 2, Content: "after\n}", IsComplete: true}})
	}
	o := normalizeOptions(PlanOptions{})
	s := newInputSizer(Document{Graph: input.Graph}, o)
	for i := 0; i < len(blocks); i++ {
		input.Batch.Blocks = blocks[:i+1]
		got := s.count(input)
		want := CountTokens(o.SystemPrompt, SerializeInput(input), ResponseSchema())
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("moving prefix %d differs: %+v / %+v", i, got, want)
		}
	}
	if s.projector.nodeMaterializations != 0 {
		t.Fatal("fit sizing materialized graph payload")
	}
}

func TestInputSizerPreservesOrdinalTieOrder(t *testing.T) {
	input := sampleBatch().Input
	input.Graph = Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Sha256: strings.Repeat("d", 64)}
	blocks := []Block{}
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("tie%d", i)
		path := "src/" + string([]byte{byte(0x80 + i%13)}) + "/A.java"
		owner := string([]byte{byte(0xc0 + i%17)})
		input.Graph.Nodes = append(input.Graph.Nodes, GraphNode{ID: id, SourceSymbolID: id, Path: path, DeclaringType: &owner, Name: id, Signature: fmt.Sprintf("int %s()", id), Kind: 4, Revision: Head, StartLine: i + 1, EndLine: i + 1})
		blocks = append(blocks, Block{ID: id, FilePath: path, SymbolIDs: []string{id}, Head: &CodeSlice{Path: path, Revision: Head, StartLine: i + 1, EndLine: i + 1, Content: "return 1;", IsComplete: true}})
	}
	o := normalizeOptions(PlanOptions{})
	s := newInputSizer(Document{Graph: input.Graph}, o)
	for i := 0; i < len(blocks); i++ {
		input.Batch.Blocks = blocks[:i+1]
		got := s.count(input)
		want := CountTokens(o.SystemPrompt, SerializeInput(input), ResponseSchema())
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ordinal tie prefix %d differs: %+v / %+v", i, got, want)
		}
	}
}

func TestInputSizerAvoidsFullWireAllocationForUniqueMixedViews(t *testing.T) {
	input := sampleBatch().Input
	input.Graph = Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Sha256: strings.Repeat("9", 64)}
	for i := 0; i < 256; i++ {
		id := fmt.Sprintf("allocation%d", i)
		change := 0
		if i < 128 {
			change = 2
		}
		input.Graph.Nodes = append(input.Graph.Nodes, GraphNode{ID: id, SourceSymbolID: id, Path: "A.java", Name: id, Signature: "int " + id + "()", Kind: 4, Revision: Head, Change: change, StartLine: i + 1, EndLine: i + 1})
	}
	inputs := []BatchInput{}
	for i := 128; i < 192; i++ {
		probe := input
		block := Block{ID: fmt.Sprintf("probe%d", i), FilePath: "A.java", SymbolIDs: []string{input.Graph.Nodes[i].ID}, Head: &CodeSlice{Path: "A.java", Revision: Head, StartLine: i + 1, EndLine: i + 1, Content: "return 1;", IsComplete: true}}
		probe.Batch.Blocks = []Block{block}
		inputs = append(inputs, probe)
	}
	o := normalizeOptions(PlanOptions{})
	s := newInputSizer(Document{Graph: input.Graph}, o)
	original := testing.AllocsPerRun(1, func() {
		for _, probe := range inputs {
			CountTokens(o.SystemPrompt, SerializeInput(probe), ResponseSchema())
		}
	})
	current := testing.AllocsPerRun(1, func() {
		clear(s.nodeWeights)
		for _, probe := range inputs {
			s.count(probe)
		}
	})
	if current >= original/4 {
		t.Fatalf("unique mixed-view allocations %.0f are not below one quarter of full wire %.0f", current, original)
	}
	t.Logf("unique mixed-view allocations: %.0f vs full wire %.0f", current, original)
}

func TestInputSizerRetainsStableWorkingSetWithinByteBudget(t *testing.T) {
	blocks := []Block{}
	for i := 0; i < 4096; i++ {
		blocks = append(blocks, Block{ID: fmt.Sprintf("stable%d", i), FilePath: "A.java", Kind: "method", Head: &CodeSlice{Path: "A.java", Revision: Head, StartLine: i + 1, EndLine: i + 1, Content: "return 1;", IsComplete: true}, SymbolIDs: []string{}, Limitations: []string{}})
	}
	s := newInputSizer(Document{Graph: sampleBatch().Input.Graph, Blocks: blocks}, normalizeOptions(PlanOptions{}))
	for _, block := range blocks {
		s.blockWeight(block)
	}
	warmBuilds := s.blockCacheBuilds
	for _, length := range []int{4095, 4096, 4094} {
		for _, block := range blocks[:length] {
			s.blockWeight(block)
		}
	}
	if s.blockCacheBuilds != warmBuilds {
		t.Fatalf("stable working set rebuilt %d entries after warmup within %d byte cache", s.blockCacheBuilds-warmBuilds, maximumBlockCacheBytes)
	}
	if s.blockCacheBytes > maximumBlockCacheBytes || s.blockCacheEntries > maximumBlockCacheEntries {
		t.Fatal("stable cache exceeded the existing retention bound")
	}
}

func TestInputSizerRetainsStableStructuralWorkingSet(t *testing.T) {
	s := newInputSizer(Document{Graph: sampleBatch().Input.Graph}, normalizeOptions(PlanOptions{}))
	b := Block{ID: "structural-working-set", FilePath: "A.java", SymbolIDs: []string{}, Limitations: []string{}}
	sources := []CodeSlice{}
	for i := 0; i < 4096; i++ {
		sources = append(sources, CodeSlice{Path: fmt.Sprintf("Context%d.java", i), Revision: Head, StartLine: 1, EndLine: 1, Content: "class Context {}", IsComplete: true})
	}
	for _, source := range sources {
		b.StructuralContext = []CodeSlice{source}
		s.blockWeight(b)
	}
	builds := s.sliceCacheBuilds
	for _, length := range []int{4095, 4096, 4094} {
		for _, source := range sources[:length] {
			b.StructuralContext = []CodeSlice{source}
			s.blockWeight(b)
		}
	}
	if s.sliceCacheBuilds != builds {
		t.Fatalf("stable source cache rebuilt %d weights within the byte budget", s.sliceCacheBuilds-builds)
	}
	if s.sliceCacheBytes > maximumSliceCacheBytes || len(s.sliceWeights) > maximumSliceCacheEntries {
		t.Fatal("source cache exceeded the existing retention bound")
	}
}

func TestProjectionIndexesSharedTypeTargetsOnce(t *testing.T) {
	owner, name := "C", "g"
	line := 500
	graph := Graph{Nodes: []GraphNode{{ID: "type", SourceSymbolID: "type", Path: "C.java", Name: owner, Kind: 0, Revision: Head, Change: 2, StartLine: 1, EndLine: 1000}, {ID: "caller", SourceSymbolID: "caller", Path: "C.java", Name: "caller", Kind: 4, Revision: Head, Change: 2, StartLine: 500, EndLine: 500}}, Edges: []GraphEdge{}}
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("method%d", i)
		graph.Nodes = append(graph.Nodes, GraphNode{ID: id, SourceSymbolID: id, Path: "C.java", DeclaringType: &owner, Name: name, Kind: 4, Revision: Head, StartLine: i + 2, EndLine: i + 2})
		graph.Edges = append(graph.Edges, GraphEdge{ID: fmt.Sprintf("edge%d", i), SourceID: "caller", TargetIDs: []string{"type"}, Kind: "call", Revision: Head, Line: &line, InvokedName: &name})
	}
	projector := newGraphProjector(graph)
	if projector.methodCandidateScans != 100 {
		t.Fatalf("shared type/name candidates scanned %d times, want 100", projector.methodCandidateScans)
	}
	graph.Nodes[1].Change = 0
	projector = newGraphProjector(graph)
	if projector.methodCandidateScans != 0 {
		t.Fatalf("unchanged source needlessly scanned %d method candidates", projector.methodCandidateScans)
	}
}
