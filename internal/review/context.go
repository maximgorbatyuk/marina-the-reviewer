package review

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

func sliceKey(s CodeSlice) string {
	return fmt.Sprintf("%s|%d|%d|%d", s.Path, s.Revision, s.StartLine, s.EndLine)
}
func normalizeContext(batch Batch, d Document) Batch {
	return newContextIndex(d).normalize(batch)
}
func (index *contextIndex) normalize(batch Batch) Batch {
	batch = index.selectImports(batch)
	batch.Blocks = append([]Block{}, batch.Blocks...)
	primary := []CodeSlice{}
	for _, block := range batch.Blocks {
		if index.operationErr() != nil {
			return Batch{}
		}
		if block.Base != nil {
			primary = append(primary, *block.Base)
		}
		if block.Head != nil {
			primary = append(primary, *block.Head)
		}
	}
	coverage, err := newContextSpanIndexContext(index.operationContext(), primary)
	if err != nil {
		return Batch{}
	}
	seen := map[contextSliceKey]bool{}
	for i, block := range batch.Blocks {
		if index.operationErr() != nil {
			return Batch{}
		}
		context := []CodeSlice{}
		for _, source := range block.StructuralContext {
			if index.operationErr() != nil {
				return Batch{}
			}
			key := contextKey(source)
			if !coverage.covers(source.Path, source.Revision, source.StartLine, source.EndLine) && !seen[key] {
				seen[key] = true
				context = append(context, source)
			}
		}
		block.StructuralContext = context
		batch.Blocks[i] = block
	}
	return batch
}

func (index *contextIndex) findBody(n GraphNode) *CodeSlice {
	for _, source := range index.sources[contextSourceKey{n.Path, n.Revision}] {
		if index.operationErr() != nil {
			return nil
		}
		if source.StartLine > n.StartLine || source.EndLine < n.EndLine {
			continue
		}
		ls := lines(source.Content)
		first, last := n.StartLine-source.StartLine, n.EndLine-source.StartLine+1
		if first < 0 || last > len(ls) {
			continue
		}
		return &CodeSlice{n.Path, n.Revision, n.StartLine, n.EndLine, strings.Join(ls[first:last], "\n"), n.StartLine, n.EndLine, true}
	}
	return nil
}
func decorateDependencies(d Document, batch Batch) Batch {
	return newContextIndex(d).decorateDependencies(batch)
}
func (index *contextIndex) decorateDependencies(batch Batch) Batch {
	d := index.document
	batch = index.normalize(batch)
	blocks := append([]Block{}, batch.Blocks...)
	nodes := map[string]GraphNode{}
	owners, err := contextOwnersContext(index.operationContext(), d.Graph, blocks)
	if err != nil {
		return Batch{}
	}
	included := map[string]bool{}
	bodies, err := contextBodyIndexContext(index.operationContext(), blocks)
	if err != nil {
		return Batch{}
	}
	for _, n := range d.Graph.Nodes {
		if index.operationErr() != nil {
			return Batch{}
		}
		nodes[n.ID] = n
		if bodies.covers(n.Path, n.Revision, n.StartLine, n.EndLine) {
			included[n.ID] = true
		}
	}
	var add func(string, int)
	add = func(id string, owner int) {
		if index.operationErr() != nil {
			return
		}
		n, ok := nodes[id]
		if !ok || included[id] {
			return
		}
		body := index.findBody(n)
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
		if index.operationErr() != nil {
			return Batch{}
		}
		if e.Kind != "call" && e.Kind != "possible-move" && e.Kind != "counterpart" {
			continue
		}
		if own := owners[e.SourceID]; len(own) > 0 {
			for _, target := range e.TargetIDs {
				if index.operationErr() != nil {
					return Batch{}
				}
				add(target, own[0])
			}
		}
		for _, target := range e.TargetIDs {
			if index.operationErr() != nil {
				return Batch{}
			}
			if own := owners[target]; len(own) > 0 {
				add(e.SourceID, own[0])
			}
		}
	}
	batch.Blocks = blocks
	return index.normalize(batch)
}
func semanticGroups(d Document) [][]Block {
	groups, _ := semanticGroupsContext(context.Background(), d)
	return groups
}
func semanticGroupsContext(ctx context.Context, d Document) ([][]Block, error) {
	related := make([][]int, len(d.Blocks))
	owners, err := contextOwnersContext(ctx, d.Graph, d.Blocks)
	if err != nil {
		return nil, err
	}
	for _, e := range d.Graph.Edges {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.Kind != "call" && e.Kind != "possible-move" && e.Kind != "counterpart" {
			continue
		}
		for _, source := range owners[e.SourceID] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for _, targetID := range e.TargetIDs {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				for _, target := range owners[targetID] {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					if source != target {
						related[source] = append(related[source], target)
						related[target] = append(related[target], source)
					}
				}
			}
		}
	}
	groups := [][]Block{}
	visited := map[int]bool{}
	for root := range d.Blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if visited[root] {
			continue
		}
		visited[root] = true
		pending := []int{root}
		group := []Block{}
		for len(pending) > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			current := pending[0]
			pending = pending[1:]
			group = append(group, d.Blocks[current])
			slices.Sort(related[current])
			for _, n := range related[current] {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if !visited[n] {
					visited[n] = true
					pending = append(pending, n)
				}
			}
		}
		groups = append(groups, group)
	}
	return groups, ctx.Err()
}

var boundaryMarkers = []string{"security", "auth", "access", "permission", "transaction", "repository", "client", "gateway", "service", "config"}

func dependencyPriority(g Graph, s CodeSlice) int {
	for _, marker := range boundaryMarkers {
		if strings.Contains(strings.ToLower(s.Path), marker) {
			return 0
		}
	}
	for _, n := range g.Nodes {
		if n.Path == s.Path && n.Revision == s.Revision && n.StartLine == s.StartLine && n.EndLine == s.EndLine {
			for _, marker := range boundaryMarkers {
				if strings.Contains(strings.ToLower(n.Name), marker) {
					return 0
				}
			}
			if n.Change == 0 {
				return 2
			}
			break
		}
	}
	return 1
}
func checkContextCoverage(d Document, batches []PreparedBatch) error {
	return checkContextCoverageContext(context.Background(), d, batches)
}
func checkContextCoverageContext(ctx context.Context, d Document, batches []PreparedBatch) error {
	for _, ch := range d.Coverage.ContextLines {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := ch.Path
		if ch.Revision == Base {
			for _, b := range d.Blocks {
				if err := ctx.Err(); err != nil {
					return err
				}
				if b.FilePath == ch.Path && b.Base != nil {
					path = b.Base.Path
					break
				}
			}
		}
		found := false
		for _, p := range batches {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, b := range p.Input.Batch.Blocks {
				if err := ctx.Err(); err != nil {
					return err
				}
				ss := append([]CodeSlice{}, b.StructuralContext...)
				if b.Base != nil {
					ss = append(ss, *b.Base)
				}
				if b.Head != nil {
					ss = append(ss, *b.Head)
				}
				for _, s := range ss {
					if err := ctx.Err(); err != nil {
						return err
					}
					if s.Path == path && s.Revision == ch.Revision && ch.Line >= s.StartLine && ch.Line <= s.EndLine {
						found = true
					}
				}
			}
		}
		if !found {
			return fmt.Errorf("changed context line was removed while packing %s/%d/%d", path, ch.Revision, ch.Line)
		}
	}
	return ctx.Err()
}

var contextWords = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
var contextImport = regexp.MustCompile(`^import\s+(?:static\s+)?[\w.$]+\.([\w$]+|\*)\s*;$`)

func isImportDeclaration(content string) bool {
	text := strings.TrimSpace(content)
	return strings.HasPrefix(text, "package ") || strings.HasPrefix(text, "import ")
}
func selectImports(batch Batch, d Document) Batch {
	return newContextIndex(d).selectImports(batch)
}
func (index *contextIndex) selectImports(batch Batch) Batch {
	d := index.document
	if d.SelectionVersion == 0 {
		return batch
	}
	isImports := func(s CodeSlice) bool { return index.imports[contextKey(s)] }
	aliases := map[string][]string{}
	for _, block := range batch.Blocks {
		if index.operationErr() != nil {
			return Batch{}
		}
		if block.Base != nil {
			aliases[block.Base.Path] = append(aliases[block.Base.Path], block.FilePath)
		}
	}
	changedLine := func(source CodeSlice, line int) bool {
		if index.changed[contextSourceKey{source.Path, source.Revision}][line] {
			return true
		}
		for _, path := range aliases[source.Path] {
			if index.operationErr() != nil {
				return false
			}
			if index.changed[contextSourceKey{path, source.Revision}][line] {
				return true
			}
		}
		return false
	}
	identifiers := map[string]bool{}
	for _, b := range batch.Blocks {
		if index.operationErr() != nil {
			return Batch{}
		}
		ss := []CodeSlice{}
		if b.Base != nil {
			ss = append(ss, *b.Base)
		}
		if b.Head != nil {
			ss = append(ss, *b.Head)
		}
		for _, s := range b.StructuralContext {
			if index.operationErr() != nil {
				return Batch{}
			}
			if !isImports(s) {
				ss = append(ss, s)
			}
		}
		for _, s := range ss {
			if index.operationErr() != nil {
				return Batch{}
			}
			for _, word := range index.wordsFor(s.Content) {
				if index.operationErr() != nil {
					return Batch{}
				}
				identifiers[word] = true
			}
		}
	}
	pattern := contextImport
	batch.Blocks = append([]Block{}, batch.Blocks...)
	for i, b := range batch.Blocks {
		if index.operationErr() != nil {
			return Batch{}
		}
		context := []CodeSlice{}
		for _, s := range b.StructuralContext {
			if index.operationErr() != nil {
				return Batch{}
			}
			if !isImports(s) {
				context = append(context, s)
				continue
			}
			ls := lines(s.Content)
			selected := []int{}
			malformed := false
			for line, text := range ls {
				if index.operationErr() != nil {
					return Batch{}
				}
				text = strings.TrimSpace(text)
				m := pattern.FindStringSubmatch(text)
				changed := changedLine(s, s.StartLine+line)
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
				if index.operationErr() != nil {
					return Batch{}
				}
				end := 1
				for end < len(selected) && selected[end] <= selected[end-1]+3 {
					if index.operationErr() != nil {
						return Batch{}
					}
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
