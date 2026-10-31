package review

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
)

func validSourcePath(value string) bool {
	return value != "" && !strings.ContainsRune(value, 0) && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}

func validateSliceContext(ctx context.Context, s CodeSlice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSourcePath(s.Path) || s.Revision != Base && s.Revision != Head {
		return fmt.Errorf("source slice has an invalid path or revision")
	}
	if s.StartLine < 1 || s.EndLine < s.StartLine || s.EndLine-s.StartLine+1 != len(lines(s.Content)) {
		return fmt.Errorf("source slice coordinates do not match its complete source lines")
	}
	if s.SymbolStartLine < 1 || s.SymbolEndLine < s.SymbolStartLine || s.StartLine < s.SymbolStartLine || s.EndLine > s.SymbolEndLine {
		return fmt.Errorf("source slice coordinates are outside its symbol bounds")
	}
	return ctx.Err()
}
func validateBlocksAndGraph(blocks []Block, g Graph) error {
	return validateBlocksAndGraphContext(context.Background(), blocks, g)
}
func validateBlocksAndGraphContext(ctx context.Context, blocks []Block, g Graph) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	nodes := map[string]GraphNode{}
	aliases := map[string]string{}
	for _, n := range g.Nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.ID == "" || n.SourceSymbolID == "" || !validSourcePath(n.Path) || n.Revision != Base && n.Revision != Head || n.StartLine < 1 || n.EndLine < n.StartLine || n.Kind < 0 || n.Kind > 4 || n.Change < 0 || n.Change > 3 {
			return fmt.Errorf("review graph node has an invalid identity or source range")
		}
		if _, exists := nodes[n.ID]; exists {
			return fmt.Errorf("review graph contains duplicate node IDs")
		}
		if _, exists := aliases[n.SourceSymbolID]; exists {
			return fmt.Errorf("review graph contains duplicate source symbol IDs")
		}
		nodes[n.ID] = n
		aliases[n.SourceSymbolID] = n.ID
	}
	for source, id := range aliases {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, exists := nodes[source]; exists && source != id {
			return fmt.Errorf("review graph node and source symbol aliases conflict")
		}
	}
	for _, n := range g.Nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.CounterpartID != nil {
			if _, exists := nodes[*n.CounterpartID]; !exists {
				return fmt.Errorf("review graph counterpart references an unknown node")
			}
		}
	}
	edgeIDs := map[string]bool{}
	for _, e := range g.Edges {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.ID == "" || edgeIDs[e.ID] {
			return fmt.Errorf("review graph contains empty or duplicate edge IDs")
		}
		edgeIDs[e.ID] = true
		source, exists := nodes[e.SourceID]
		if !exists {
			return fmt.Errorf("review graph edge references an unknown source node")
		}
		if e.Revision != Base && e.Revision != Head || e.Revision != source.Revision || e.Resolution < 0 || e.Resolution > 2 || !slices.Contains([]string{"call", "contains", "counterpart", "possible-move"}, e.Kind) {
			return fmt.Errorf("review graph edge has an invalid kind or revision")
		}
		if e.Line != nil && (*e.Line < source.StartLine || *e.Line > source.EndLine) {
			return fmt.Errorf("review graph edge line is outside its source symbol")
		}
		targets := map[string]bool{}
		for _, id := range e.TargetIDs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, exists := nodes[id]; !exists {
				return fmt.Errorf("review graph edge references an unknown target node")
			}
			if targets[id] {
				return fmt.Errorf("review graph edge contains duplicate target node IDs")
			}
			targets[id] = true
		}
	}
	blockIDs := map[string]bool{}
	changed := map[string]bool{}
	type sourceRange struct{ first, last int }
	ranges := map[string][]sourceRange{}
	for _, b := range blocks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if b.ID == "" || blockIDs[b.ID] || !validSourcePath(b.FilePath) {
			return fmt.Errorf("review blocks contain an invalid path or duplicate block ID")
		}
		blockIDs[b.ID] = true
		if b.Base == nil && b.Head == nil {
			return fmt.Errorf("review block has no primary source")
		}
		if b.PartIndex < 0 || b.PartCount < 0 || b.PartCount > 0 && (b.PartIndex < 1 || b.PartIndex > b.PartCount) {
			return fmt.Errorf("review block part coordinates are invalid")
		}
		symbols := map[string]bool{}
		for _, id := range b.SymbolIDs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, exists := nodes[id]; !exists {
				if _, exists = aliases[id]; !exists {
					return fmt.Errorf("review block references an unknown graph symbol")
				}
			}
			if symbols[id] {
				return fmt.Errorf("review block contains duplicate graph symbols")
			}
			symbols[id] = true
		}
		for i, s := range []*CodeSlice{b.Base, b.Head} {
			if err := ctx.Err(); err != nil {
				return err
			}
			if s == nil {
				continue
			}
			if e := validateSliceContext(ctx, *s); e != nil {
				return e
			}
			if s.Revision != Revision(i) {
				return fmt.Errorf("review block source has the wrong revision")
			}
			if i == 0 && !b.BaseExists || i == 1 && !b.HeadExists {
				return fmt.Errorf("review block source conflicts with its existence flag")
			}
			if i == 1 && s.Path != b.FilePath {
				return fmt.Errorf("review block head path does not match its file path")
			}
			key := fmt.Sprintf("%s|%d", s.Path, s.Revision)
			ranges[key] = append(ranges[key], sourceRange{s.StartLine, s.EndLine})
		}
		for _, s := range b.StructuralContext {
			if err := ctx.Err(); err != nil {
				return err
			}
			if e := validateSliceContext(ctx, s); e != nil {
				return e
			}
		}
		for _, ch := range b.ChangedLines {
			if err := ctx.Err(); err != nil {
				return err
			}
			s := b.Base
			if ch.Revision == Head {
				s = b.Head
			} else if ch.Revision != Base {
				return fmt.Errorf("changed line has an invalid source revision")
			}
			if s == nil || ch.Line < s.StartLine || ch.Line > s.EndLine {
				return fmt.Errorf("changed line is outside its primary source slice")
			}
			key := fmt.Sprintf("%s|%d|%d", s.Path, ch.Revision, ch.Line)
			if changed[key] {
				return fmt.Errorf("review blocks contain duplicate changed-line ownership")
			}
			changed[key] = true
		}
	}
	for _, spans := range ranges {
		if err := ctx.Err(); err != nil {
			return err
		}
		slices.SortFunc(spans, func(a, b sourceRange) int { return a.first - b.first })
		for i := 1; i < len(spans); i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if spans[i].first <= spans[i-1].last {
				return fmt.Errorf("review blocks contain overlapping primary source ranges")
			}
		}
	}
	return ctx.Err()
}

// ValidateDocument uses a background context for callers without cancellation support.
func ValidateDocument(d Document) error {
	return ValidateDocumentContext(context.Background(), d)
}

// ValidateDocumentContext checks source coverage and graph consistency before a document is planned.
func ValidateDocumentContext(ctx context.Context, d Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.Version != 3 {
		return fmt.Errorf("unsupported review document version")
	}
	if e := validateBlocksAndGraphContext(ctx, d.Blocks, d.Graph); e != nil {
		return e
	}
	for _, s := range append(append([]CodeSlice{}, d.DependencyContext...), d.DeclarationContext...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e := validateSliceContext(ctx, s); e != nil {
			return e
		}
	}
	return ctx.Err()
}
