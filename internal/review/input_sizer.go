package review

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

const (
	maximumSliceCacheBytes   = 8 << 20
	maximumBlockCacheBytes   = 16 << 20
	maximumSliceCacheEntries = 65536
	maximumBlockCacheEntries = 65536
)

type edgeSize struct {
	group     int
	head, row int
}
type blockSize struct {
	block  Block
	weight int
}
type inputSizer struct {
	projector           *graphProjector
	nodes               *nodeSizer
	graphBaseWeight     int
	system, schema      int
	all                 map[string]GraphNode
	aliases             map[string]string
	nodeWeights         map[string]int
	edgeWeights         []edgeSize
	sliceWeights        map[CodeSlice]int
	blockWeights        map[string][]blockSize
	graphTemplate       map[string]any
	nodeCatalogueBuilds int
	sliceCacheBytes     int
	sliceCacheBuilds    int
	blockCacheBytes     int
	blockCacheEntries   int
	blockCacheBuilds    int
	edgeGroupSeen       []uint64
	edgeGeneration      uint64
}

func jsonWeight(value any) int { return stringWeight(canonicalJSON(value)) }
func jsonWeightContext(ctx context.Context, value any) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	wire := canonicalJSON(value)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return stringWeightContext(ctx, wire)
}
func stringWeightContext(ctx context.Context, value string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	weight := 0
	var steps uint32
	for _, r := range value {
		steps++
		if steps&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		if r < 128 {
			weight += 3
		} else {
			weight += 12
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return weight, nil
}

// Keep the weight before rounding. A package has one final user-token rounding.
func stringWeight(value string) int {
	weight := 0
	for _, r := range value {
		if r < 128 {
			weight += 3
		} else {
			weight += 12
		}
	}
	return weight
}
func integerWeight(value int) int {
	weight := 3
	if value < 0 {
		weight += 3
		for value <= -10 {
			weight += 3
			value /= 10
		}
		return weight
	}
	for value >= 10 {
		weight += 3
		value /= 10
	}
	return weight
}
func newInputSizer(d Document, o PlanOptions) *inputSizer {
	value, _ := newInputSizerContext(context.Background(), d, o)
	return value
}
func newInputSizerContext(ctx context.Context, d Document, o PlanOptions) (*inputSizer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	poll := cpuContextPoll{context: ctx}
	projector, err := newGraphProjectorContext(ctx, d.Graph)
	if err != nil {
		return nil, err
	}
	nodes, err := newNodeSizerContext(ctx, d.Graph)
	if err != nil {
		return nil, err
	}
	system, err := calibratedContext(ctx, o.SystemPrompt)
	if err != nil {
		return nil, err
	}
	schema, err := calibratedContext(ctx, string(ResponseSchema()))
	if err != nil {
		return nil, err
	}

	s := &inputSizer{projector: projector, nodes: nodes, system: system, schema: schema + 128, all: map[string]GraphNode{}, aliases: map[string]string{}, nodeWeights: map[string]int{}, edgeWeights: make([]edgeSize, len(d.Graph.Edges)), sliceWeights: map[CodeSlice]int{}, blockWeights: map[string][]blockSize{}}
	for _, n := range d.Graph.Nodes {
		if err := poll.check(); err != nil {
			return nil, err
		}
		s.all[n.ID] = n
		s.aliases[n.SourceSymbolID] = n.ID
	}
	groups := map[string]int{}
	for i, e := range d.Graph.Edges {
		if err := poll.check(); err != nil {
			return nil, err
		}
		kind := map[string]int{"call": 0, "contains": 1, "counterpart": 2, "possible-move": 3}[e.Kind]
		change := map[string]int{"unchanged": 0, "added": 1, "removed": 2, "structural": 3, "matched-declaration": 4, "unproven": 5}[e.Change]
		head := canonicalJSON([]any{kind, e.TargetIDs, e.Revision, e.InvokedName, e.ReceiverType, e.Resolution, change, e.Confidence, e.Reasons})
		group, ok := groups[head]
		if !ok {
			group = len(groups)
			groups[head] = group
		}
		s.edgeWeights[i] = edgeSize{group, stringWeight(head), jsonWeight([]any{e.ID, e.SourceID, e.Line})}
	}
	s.edgeGroupSeen = make([]uint64, len(groups))
	var root map[string]json.RawMessage
	_ = json.Unmarshal([]byte(SerializeInput(BatchInput{Version: 3, Graph: Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Limitations: []string{}}, Task: json.RawMessage(`null`)})), &root)
	_ = json.Unmarshal(root["graph"], &s.graphTemplate)
	s.graphTemplate["fullGraphSha256"] = d.Graph.Sha256
	s.graphTemplate["fullNodeCount"] = len(d.Graph.Nodes)
	s.graphTemplate["fullEdgeCount"] = len(d.Graph.Edges)
	s.graphTemplate["changedNodeCount"] = s.projector.changed
	s.graphTemplate["limitations"] = s.projector.limitations
	s.graphTemplate["sha256"] = strings.Repeat("0", 64)
	s.graphTemplate["includedNodeCount"] = 0
	s.graphTemplate["includedEdgeCount"] = 0
	s.graphTemplate["isCompleteGraph"] = false
	s.graphBaseWeight = jsonWeight(s.graphTemplate)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *inputSizer) graphWeightContext(ctx context.Context, selected projectionSelection, edgesWeight int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	key := string(selected.membership)
	weight, ok := s.nodeWeights[key]
	if !ok {
		nodeWeight, err := s.nodes.weightContext(ctx, selected.membership)
		if err != nil {
			return 0, err
		}
		weight = s.graphBaseWeight + nodeWeight - 24 + integerWeight(selected.nodeCount) - 3
		if len(s.nodeWeights) >= 128 {
			clear(s.nodeWeights)
		}
		s.nodeWeights[key] = weight
		s.nodeCatalogueBuilds++
	}
	weight += edgesWeight - 6 + integerWeight(selected.edgeCount) - 3
	if selected.nodeCount == len(s.projector.full.Nodes) && selected.edgeCount == len(s.projector.full.Edges) {
		weight -= 3
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return weight, nil
}
func sameSizeBlock(a, b Block) bool {
	sourceEqual := func(a, b *CodeSlice) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return *a == *b
	}
	return a.ID == b.ID && a.FilePath == b.FilePath && a.Kind == b.Kind && a.BaseExists == b.BaseExists && a.HeadExists == b.HeadExists && a.PartIndex == b.PartIndex && a.PartCount == b.PartCount && sourceEqual(a.Base, b.Base) && sourceEqual(a.Head, b.Head) && slices.Equal(a.SymbolIDs, b.SymbolIDs) && slices.Equal(a.ChangedLines, b.ChangedLines) && (a.Limitations == nil) == (b.Limitations == nil) && slices.Equal(a.Limitations, b.Limitations)
}
func retainedBlockBytes(b Block) int {
	retained := len(b.ID) + len(b.FilePath) + len(b.Kind) + len(b.ChangedLines)*16 + 256
	for _, values := range [][]string{b.SymbolIDs, b.Limitations} {
		for _, value := range values {
			retained += len(value) + 16
		}
	}
	for _, source := range []*CodeSlice{b.Base, b.Head} {
		if source != nil {
			retained += len(source.Path) + len(source.Content) + 128
		}
	}
	return retained
}

func (s *inputSizer) blockWeight(b Block) int {
	weight, _ := s.blockWeightContext(context.Background(), b)
	return weight
}
func (s *inputSizer) blockWeightContext(ctx context.Context, b Block) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	poll := cpuContextPoll{context: ctx}

	weight := 0
	found := false
	for _, cached := range s.blockWeights[b.ID] {
		if err := poll.check(); err != nil {
			return 0, err
		}
		if sameSizeBlock(cached.block, b) {
			weight = cached.weight
			found = true
			break
		}
	}
	if !found {
		s.blockCacheBuilds++
		plain := b
		plain.StructuralContext = nil
		block, err := makeWireBlockContext(ctx, plain, s.all, s.aliases)
		if err != nil {
			return 0, err
		}
		weight, err = jsonWeightContext(ctx, block)
		if err != nil {
			return 0, err
		}
		plain.SymbolIDs = slices.Clone(b.SymbolIDs)
		plain.ChangedLines = slices.Clone(b.ChangedLines)
		plain.Limitations = slices.Clone(b.Limitations)
		if b.Base != nil {
			cloned := *b.Base
			plain.Base = &cloned
		}
		if b.Head != nil {
			cloned := *b.Head
			plain.Head = &cloned
		}
		retained := retainedBlockBytes(plain)
		if retained <= maximumBlockCacheBytes {
			if s.blockCacheEntries >= maximumBlockCacheEntries || s.blockCacheBytes+retained > maximumBlockCacheBytes {
				clear(s.blockWeights)
				s.blockCacheBytes, s.blockCacheEntries = 0, 0
			}
			s.blockWeights[b.ID] = append(s.blockWeights[b.ID], blockSize{plain, weight})
			s.blockCacheBytes += retained
			s.blockCacheEntries++
		}
	}
	for i, source := range b.StructuralContext {
		if err := poll.check(); err != nil {
			return 0, err
		}
		part, ok := s.sliceWeights[source]
		if !ok {
			s.sliceCacheBuilds++
			revision := "Base"
			if source.Revision == Head {
				revision = "Head"
			}
			var err error
			part, err = jsonWeightContext(ctx, structural{revision, makeWireSlice(&source)})
			if err != nil {
				return 0, err
			}
			retained := len(source.Path) + len(source.Content) + 128
			if retained <= maximumSliceCacheBytes {
				if len(s.sliceWeights) >= maximumSliceCacheEntries || s.sliceCacheBytes+retained > maximumSliceCacheBytes {
					clear(s.sliceWeights)
					s.sliceCacheBytes = 0
				}
				s.sliceWeights[source] = part
				s.sliceCacheBytes += retained
			}
		}
		if i > 0 {
			weight += 3
		}
		weight += part
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return weight, nil
}
func (s *inputSizer) count(input BatchInput) TokenCount {
	count, _ := s.countContext(context.Background(), input)
	return count
}
func (s *inputSizer) countContext(ctx context.Context, input BatchInput) (TokenCount, error) {
	if err := ctx.Err(); err != nil {
		return TokenCount{}, err
	}
	poll := cpuContextPoll{context: ctx}

	s.edgeGeneration++
	if s.edgeGeneration == 0 {
		clear(s.edgeGroupSeen)
		s.edgeGeneration = 1
	}
	generation := s.edgeGeneration
	edgesWeight, groups := 6, 0
	selected, err := s.projector.selectGraphContext(ctx, input.Batch, func(edge int) {
		entry := s.edgeWeights[edge]
		if s.edgeGroupSeen[entry.group] != generation {
			s.edgeGroupSeen[entry.group] = generation
			if groups > 0 {
				edgesWeight += 3
			}
			groups++
			edgesWeight += entry.head + 9 + entry.row
		} else {
			edgesWeight += 3 + entry.row
		}
	})
	if err != nil {
		return TokenCount{}, err
	}
	limitations := []string{}
	for _, value := range input.Limitations {
		if err := poll.check(); err != nil {
			return TokenCount{}, err
		}
		if !slices.Contains(s.projector.limitations, value) {
			limitations = append(limitations, value)
		}
	}
	batch := struct {
		Index         int        `json:"index"`
		Count         int        `json:"count"`
		IsWholeReview bool       `json:"isWholeReview"`
		FileParts     []FilePart `json:"fileParts"`
		Blocks        []any      `json:"blocks"`
	}{input.Batch.Index, input.Batch.Count, input.Batch.IsWholeReview, input.Batch.FileParts, []any{}}
	weight, err := jsonWeightContext(ctx, struct {
		Version     int             `json:"version"`
		Purpose     string          `json:"purpose"`
		BaseSha     string          `json:"baseSha"`
		HeadSha     string          `json:"headSha"`
		Graph       any             `json:"graph"`
		Batch       any             `json:"batch"`
		Limitations []string        `json:"limitations"`
		Task        json.RawMessage `json:"task"`
	}{input.Version, "ArchitectureReview", input.BaseSha, input.HeadSha, nil, batch, limitations, input.Task})
	if err != nil {
		return TokenCount{}, err
	}
	graphWeight, err := s.graphWeightContext(ctx, selected, edgesWeight)
	if err != nil {
		return TokenCount{}, err
	}
	weight = weight - 12 + graphWeight
	for i, b := range input.Batch.Blocks {
		if err := poll.check(); err != nil {
			return TokenCount{}, err
		}
		if i > 0 {
			weight += 3
		}
		blockWeight, err := s.blockWeightContext(ctx, b)
		if err != nil {
			return TokenCount{}, err
		}
		weight += blockWeight
	}
	if err := ctx.Err(); err != nil {
		return TokenCount{}, err
	}
	return TokenCount{TemplatedInputTokens: s.system + (weight+7)/8 + 256, SchemaReserveTokens: s.schema, Method: "estimate:unicode-aware-1.5x+256-chat-overhead", Limitation: "No verified provider tokenizer is available. The estimate uses a 1.5 safety factor and relies on bounded context-overflow replanning.", SchemaTokenizationMethod: "estimate:unicode-aware-1.5x+128-wrapper"}, nil
}
