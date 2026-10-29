package review

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

func containsNode(b Block, n GraphNode) bool {
	return (slices.Contains(b.SymbolIDs, n.ID) || slices.Contains(b.SymbolIDs, n.SourceSymbolID)) && (intersects(n, b.Base) || intersects(n, b.Head))
}
func sliceKey(s CodeSlice) string {
	return fmt.Sprintf("%s|%d|%d|%d", s.Path, s.Revision, s.StartLine, s.EndLine)
}
func normalizeContext(batch Batch, d Document) Batch {
	batch = selectImports(batch, d)
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
			key := sliceKey(s)
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
func findBody(d Document, n GraphNode) *CodeSlice {
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
func decorateDependencies(d Document, batch Batch) Batch {
	batch = normalizeContext(batch, d)
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
			if containsNode(b, n) {
				owners[n.ID] = append(owners[n.ID], i)
			}
		}
		if hasBody(n, bodyBlocks) {
			included[n.ID] = true
		}
	}
	var add func(string, int)
	add = func(id string, owner int) {
		n, ok := nodes[id]
		if !ok || included[id] {
			return
		}
		body := findBody(d, n)
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
	return normalizeContext(batch, d)
}
func semanticGroups(d Document) [][]Block {
	related := make([][]int, len(d.Blocks))
	owners := map[string][]int{}
	for _, n := range d.Graph.Nodes {
		for i, b := range d.Blocks {
			if containsNode(b, n) {
				owners[n.ID] = append(owners[n.ID], i)
			}
		}
	}
	for _, e := range d.Graph.Edges {
		if e.Kind != "call" && e.Kind != "possible-move" && e.Kind != "counterpart" {
			continue
		}
		for _, source := range owners[e.SourceID] {
			for _, targetID := range e.TargetIDs {
				for _, target := range owners[targetID] {
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
		if visited[root] {
			continue
		}
		visited[root] = true
		pending := []int{root}
		group := []Block{}
		for len(pending) > 0 {
			current := pending[0]
			pending = pending[1:]
			group = append(group, d.Blocks[current])
			slices.Sort(related[current])
			for _, n := range related[current] {
				if !visited[n] {
					visited[n] = true
					pending = append(pending, n)
				}
			}
		}
		groups = append(groups, group)
	}
	return groups
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
	for _, ch := range d.Coverage.ContextLines {
		path := ch.Path
		if ch.Revision == Base {
			for _, b := range d.Blocks {
				if b.FilePath == ch.Path && b.Base != nil {
					path = b.Base.Path
					break
				}
			}
		}
		found := false
		for _, p := range batches {
			for _, b := range p.Input.Batch.Blocks {
				ss := append([]CodeSlice{}, b.StructuralContext...)
				if b.Base != nil {
					ss = append(ss, *b.Base)
				}
				if b.Head != nil {
					ss = append(ss, *b.Head)
				}
				for _, s := range ss {
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
	return nil
}
func selectImports(batch Batch, d Document) Batch {
	if d.SelectionVersion == 0 {
		return batch
	}
	isImports := func(s CodeSlice) bool {
		for _, decl := range d.DeclarationContext {
			if sliceKey(decl) == sliceKey(s) && (strings.HasPrefix(strings.TrimSpace(decl.Content), "package ") || strings.HasPrefix(strings.TrimSpace(decl.Content), "import ")) {
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
