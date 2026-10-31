package review

import (
	"context"
)

type contextSliceKey struct {
	path       string
	revision   Revision
	start, end int
}

func contextKey(s CodeSlice) contextSliceKey {
	return contextSliceKey{s.Path, s.Revision, s.StartLine, s.EndLine}
}

type contextSourceKey struct {
	path     string
	revision Revision
}

type contextInterval struct{ start, end int }
type contextSpanIndex map[contextSourceKey][]contextInterval

func newContextSpanIndexContext(ctx context.Context, sources []CodeSlice) (contextSpanIndex, error) {
	index := contextSpanIndex{}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := contextSourceKey{source.Path, source.Revision}
		index[key] = append(index[key], contextInterval{source.StartLine, source.EndLine})
	}
	if err := index.sealContext(ctx); err != nil {
		return nil, err
	}
	return index, ctx.Err()
}

func (index contextSpanIndex) sealContext(ctx context.Context) error {
	for key, spans := range index {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sortWithContext(ctx, spans, func(a, b contextInterval) int {
			if a.start < b.start {
				return -1
			}
			if a.start > b.start {
				return 1
			}
			return 0
		}, false); err != nil {
			return err
		}
		for i := 1; i < len(spans); i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			spans[i].end = max(spans[i-1].end, spans[i].end)
		}
		index[key] = spans
	}
	return ctx.Err()
}

func (index contextSpanIndex) covers(path string, revision Revision, start, end int) bool {
	spans := index[contextSourceKey{path, revision}]
	low, high := 0, len(spans)
	for low < high {
		middle := low + (high-low)/2
		if spans[middle].start <= start {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low > 0 && spans[low-1].end >= end
}

type contextIndex struct {
	document         Document
	imports          map[contextSliceKey]bool
	changed          map[contextSourceKey]map[int]bool
	sources          map[contextSourceKey][]CodeSlice
	originalText     map[string]bool
	wordCache        map[string][]string
	wordCacheBytes   int
	importCache      map[string]contextImportText
	importCacheBytes int
	trial            *contextTrialState
	operation        context.Context
}

func newContextIndex(document Document) *contextIndex {
	index, _ := newContextIndexContext(context.Background(), document)
	return index
}

func newContextIndexContext(ctx context.Context, document Document) (*contextIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	index := &contextIndex{document: document, imports: map[contextSliceKey]bool{},
		changed: map[contextSourceKey]map[int]bool{}, sources: map[contextSourceKey][]CodeSlice{},
		originalText: map[string]bool{}, wordCache: map[string][]string{}, importCache: map[string]contextImportText{}}
	if ctx.Done() != nil {
		index.operation = ctx
	}
	for _, declaration := range document.DeclarationContext {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if isImportDeclaration(declaration.Content) {
			index.imports[contextKey(declaration)] = true
		}
	}
	for _, line := range document.Coverage.ContextLines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := contextSourceKey{line.Path, line.Revision}
		if index.changed[key] == nil {
			index.changed[key] = map[int]bool{}
		}
		index.changed[key][line.Line] = true
	}
	add := func(source CodeSlice) {
		key := contextSourceKey{source.Path, source.Revision}
		index.sources[key] = append(index.sources[key], source)
		index.originalText[source.Content] = true
	}
	for _, block := range document.Blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if block.Base != nil {
			add(*block.Base)
		}
		if block.Head != nil {
			add(*block.Head)
		}
		for _, source := range block.StructuralContext {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			index.originalText[source.Content] = true
		}
	}
	for _, source := range document.DependencyContext {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		add(source)
	}
	return index, ctx.Err()
}

func (index *contextIndex) operationErr() error {
	if index.operation == nil {
		return nil
	}
	return index.operation.Err()
}

func (index *contextIndex) operationContext() context.Context {
	if index.operation == nil {
		return context.Background()
	}
	return index.operation
}

func (index *contextIndex) normalizeContext(ctx context.Context, batch Batch) (Batch, error) {
	previous := index.operation
	index.operation = ctx
	defer func() { index.operation = previous }()
	if err := ctx.Err(); err != nil {
		return Batch{}, err
	}
	result := index.normalize(batch)
	if err := ctx.Err(); err != nil {
		return Batch{}, err
	}
	return result, nil
}

func (index *contextIndex) normalizeTrialContext(ctx context.Context, batch Batch) (Batch, error) {
	previous := index.operation
	index.operation = ctx
	defer func() { index.operation = previous }()
	if err := ctx.Err(); err != nil {
		return Batch{}, err
	}
	result := index.normalizeTrial(batch)
	if err := ctx.Err(); err != nil {
		return Batch{}, err
	}
	return result, nil
}

func (index *contextIndex) decorateDependenciesContext(ctx context.Context, batch Batch) (Batch, error) {
	previous := index.operation
	index.operation = ctx
	defer func() { index.operation = previous }()
	if err := ctx.Err(); err != nil {
		return Batch{}, err
	}
	result := index.decorateDependencies(batch)
	if err := ctx.Err(); err != nil {
		return Batch{}, err
	}
	return result, nil
}

const contextWordCacheMaximumBytes = 8 * 1024 * 1024

func (index *contextIndex) wordsFor(content string) []string {
	if words, ok := index.wordCache[content]; ok {
		return words
	}
	if index.operationErr() != nil {
		return nil
	}
	words := contextWords.FindAllString(content, -1)
	if index.operationErr() != nil {
		return nil
	}
	cost := 128 + cap(words)*16
	// Keep input text only so split trials cannot retain more text variants.
	if index.originalText[content] && cost <= contextWordCacheMaximumBytes-index.wordCacheBytes {
		index.wordCache[content] = words
		index.wordCacheBytes += cost
	}
	return words
}

func contextBodyIndex(blocks []Block) contextSpanIndex {
	value, _ := contextBodyIndexContext(context.Background(), blocks)
	return value
}
func contextBodyIndexContext(ctx context.Context, blocks []Block) (contextSpanIndex, error) {
	sources := []CodeSlice{}
	for _, block := range blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, source := range block.StructuralContext {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if source.IsComplete {
				sources = append(sources, source)
			}
		}
		if block.Base != nil && block.Base.IsComplete && block.Base.Revision != Head {
			sources = append(sources, *block.Base)
		}
		if block.Head != nil && block.Head.IsComplete && block.Head.Revision == Head {
			sources = append(sources, *block.Head)
		}
	}
	return newContextSpanIndexContext(ctx, sources)
}

func contextOwners(graph Graph, blocks []Block) map[string][]int {
	owners, _ := contextOwnersContext(context.Background(), graph, blocks)
	return owners
}
func contextOwnersContext(ctx context.Context, graph Graph, blocks []Block) (map[string][]int, error) {
	bySymbol := map[string][]int{}
	for i, block := range blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, id := range block.SymbolIDs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !seen[id] {
				bySymbol[id] = append(bySymbol[id], i)
				seen[id] = true
			}
		}
	}
	owners := map[string][]int{}
	for _, node := range graph.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		left, right := bySymbol[node.ID], bySymbol[node.SourceSymbolID]
		for a, b := 0, 0; a < len(left) || b < len(right); {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var index int
			if b == len(right) || a < len(left) && left[a] < right[b] {
				index = left[a]
				a++
			} else if a == len(left) || right[b] < left[a] {
				index = right[b]
				b++
			} else {
				index = left[a]
				a++
				b++
			}
			if intersects(node, blocks[index].Base) || intersects(node, blocks[index].Head) {
				owners[node.ID] = append(owners[node.ID], index)
			}
		}
	}
	return owners, ctx.Err()
}
