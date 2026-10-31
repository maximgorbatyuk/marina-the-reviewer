package review

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

const CurrentSelectionVersion = 2

func selectMRGraph(full Graph, symbols []javaSymbol) (Graph, map[string]string, map[string]bool) {
	graph, ids, bodies, _ := selectMRGraphContext(context.Background(), full, symbols)
	return graph, ids, bodies
}
func selectMRGraphContext(ctx context.Context, full Graph, symbols []javaSymbol) (Graph, map[string]string, map[string]bool, error) {
	nodes := make(map[string]GraphNode, len(full.Nodes))
	parents := map[string]string{}
	included := map[string]bool{}
	for _, node := range full.Nodes {
		if err := ctx.Err(); err != nil {
			return Graph{}, nil, nil, err
		}
		nodes[node.ID] = node
		if node.Change != 0 {
			included[node.ID] = true
		}
	}
	for _, edge := range full.Edges {
		if err := ctx.Err(); err != nil {
			return Graph{}, nil, nil, err
		}
		if edge.Kind == "contains" {
			for _, target := range edge.TargetIDs {
				parents[target] = edge.SourceID
			}
		}
	}
	calls := []GraphEdge{}
	for _, edge := range full.Edges {
		if err := ctx.Err(); err != nil {
			return Graph{}, nil, nil, err
		}
		if edge.Kind != "call" {
			continue
		}
		related := nodes[edge.SourceID].Change != 0
		for _, target := range edge.TargetIDs {
			related = related || nodes[target].Change != 0
		}
		if related {
			calls = append(calls, edge)
		}
	}
	moves, err := possibleMoveEdgesContext(ctx, symbols)
	if err != nil {
		return Graph{}, nil, nil, err
	}
	relevantBodies := map[string]bool{}
	for _, edge := range append(append([]GraphEdge{}, calls...), moves...) {
		if err := ctx.Err(); err != nil {
			return Graph{}, nil, nil, err
		}
		included[edge.SourceID] = true
		relevantBodies[nodes[edge.SourceID].SourceSymbolID] = true
		for _, target := range edge.TargetIDs {
			included[target] = true
			relevantBodies[nodes[target].SourceSymbolID] = true
		}
	}
	pending := []string{}
	for id := range included {
		if err := ctx.Err(); err != nil {
			return Graph{}, nil, nil, err
		}
		pending = append(pending, id)
	}
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return Graph{}, nil, nil, err
		}
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		ancestors := []string{parents[id]}
		if counterpart := nodes[id].CounterpartID; counterpart != nil {
			ancestors = append(ancestors, *counterpart)
		}
		for _, ancestor := range ancestors {
			if ancestor != "" && !included[ancestor] {
				included[ancestor] = true
				pending = append(pending, ancestor)
			}
		}
	}
	selected := Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Limitations: full.Limitations}
	ids, sourceIDs := map[string]string{}, map[string]string{}
	for _, node := range full.Nodes {
		if err := ctx.Err(); err != nil {
			return Graph{}, nil, nil, err
		}
		if included[node.ID] {
			ids[node.ID] = fmt.Sprintf("n%d", len(ids)+1)
			sourceIDs[node.SourceSymbolID] = ids[node.ID]
		}
	}
	add := func(edge GraphEdge) {
		edge.ID = fmt.Sprintf("e%d", len(selected.Edges)+1)
		edge.SourceID = ids[edge.SourceID]
		targets := make([]string, len(edge.TargetIDs))
		for i, id := range edge.TargetIDs {
			targets[i] = ids[id]
		}
		edge.TargetIDs = targets
		selected.Edges = append(selected.Edges, edge)
	}
	for _, node := range full.Nodes {
		if err := ctx.Err(); err != nil {
			return Graph{}, nil, nil, err
		}
		if !included[node.ID] {
			continue
		}
		originalID := node.ID
		node.ID = ids[originalID]
		if node.CounterpartID != nil {
			counterpart := ids[*node.CounterpartID]
			node.CounterpartID = &counterpart
		}
		if parent, found := nodes[parents[originalID]]; found {
			owner := parent.Name
			node.DeclaringType = &owner
			add(GraphEdge{Kind: "contains", SourceID: parent.ID, TargetIDs: []string{originalID}, Revision: node.Revision, Resolution: 0, Change: "structural"})
		}
		if node.Revision == Base && node.CounterpartID != nil {
			add(GraphEdge{Kind: "counterpart", SourceID: originalID, TargetIDs: []string{*nodes[originalID].CounterpartID}, Revision: Base, Resolution: 0, Change: "matched-declaration"})
		}
		selected.Nodes = append(selected.Nodes, node)
	}
	slices.SortStableFunc(calls, func(a, b GraphEdge) int {
		if order := ordinalCompare(ids[a.SourceID], ids[b.SourceID]); order != 0 {
			return order
		}
		if a.Line != nil && b.Line != nil && *a.Line != *b.Line {
			return *a.Line - *b.Line
		}
		if a.InvokedName != nil && b.InvokedName != nil {
			return ordinalCompare(*a.InvokedName, *b.InvokedName)
		}
		if a.InvokedName == nil && b.InvokedName != nil {
			return -1
		}
		if b.InvokedName == nil && a.InvokedName != nil {
			return 1
		}
		return 0
	})
	for _, edge := range calls {
		if err := ctx.Err(); err != nil {
			return Graph{}, nil, nil, err
		}
		add(edge)
	}
	slices.SortStableFunc(moves, func(a, b GraphEdge) int {
		if order := ordinalCompare(a.SourceID, b.SourceID); order != 0 {
			return order
		}
		return ordinalCompare(a.TargetIDs[0], b.TargetIDs[0])
	})
	for _, edge := range moves {
		if err := ctx.Err(); err != nil {
			return Graph{}, nil, nil, err
		}
		add(edge)
	}
	return selected, sourceIDs, relevantBodies, ctx.Err()
}

var moveTokenPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

func possibleMoveEdges(symbols []javaSymbol) []GraphEdge {
	result, _ := possibleMoveEdgesContext(context.Background(), symbols)
	return result
}
func possibleMoveEdgesContext(ctx context.Context, symbols []javaSymbol) ([]GraphEdge, error) {
	type moveSource struct {
		symbol    javaSymbol
		tokens    map[string]bool
		body      string
		signature string
	}
	deleted, added := []moveSource{}, []moveSource{}
	for _, symbol := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if symbol.node.Kind != 4 || symbol.node.Change != 1 && symbol.node.Change != 3 {
			continue
		}
		tokens := map[string]bool{}
		for _, token := range moveTokenPattern.FindAllString(symbol.content, -1) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !isJavaInvocationKeyword(token) {
				tokens[token] = true
			}
		}
		body := ""
		if opening := javaBodyOpening(symbol.content); opening >= 0 {
			body = strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, symbol.content[opening:])
		}
		key := strings.SplitN(symbol.key, ":", 3)
		signature := symbol.node.Signature
		if len(key) == 3 {
			signature = key[2]
		}
		entry := moveSource{symbol, tokens, body, signature}
		if symbol.node.Change == 3 {
			deleted = append(deleted, entry)
		} else {
			added = append(added, entry)
		}
	}
	byName, byBody := map[string][]moveSource{}, map[string][]moveSource{}
	for _, entry := range added {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		byName[entry.symbol.node.Name] = append(byName[entry.symbol.node.Name], entry)
		if len(entry.body) > 4 {
			byBody[entry.body] = append(byBody[entry.body], entry)
		}
	}
	result := []GraphEdge{}
	add := func(before, after moveSource, confidence float64, reasons []string) {
		result = append(result, GraphEdge{Kind: "possible-move", SourceID: before.symbol.node.SourceSymbolID, TargetIDs: []string{after.symbol.node.SourceSymbolID}, Revision: Base, Resolution: 1, Change: "unproven", Confidence: &confidence, Reasons: reasons})
	}
	for _, before := range deleted {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, after := range byName[before.symbol.node.Name] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			intersection := 0
			for token := range before.tokens {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if after.tokens[token] {
					intersection++
				}
			}
			similarity := 0.0
			if union := len(before.tokens) + len(after.tokens) - intersection; union > 0 && len(before.tokens) > 0 && len(after.tokens) > 0 {
				similarity = float64(intersection) / float64(union)
			}
			matches := before.signature == after.signature
			if !matches && similarity < 0.65 || similarity < 0.45 {
				continue
			}
			confidence, reason := similarity, "matching-name"
			if matches {
				confidence, reason = max(0.75, similarity), "matching-signature"
			}
			confidence = math.RoundToEven(confidence*1000) / 1000
			add(before, after, confidence, []string{reason, fmt.Sprintf("body-similarity:%.3f", similarity)})
		}
		if len(before.body) > 4 {
			for _, after := range byBody[before.body] {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if before.symbol.node.Name != after.symbol.node.Name {
					add(before, after, 0.7, []string{"renamed-declaration", "matching-body-text-not-behavior"})
				}
			}
		}
	}
	return result, ctx.Err()
}

func javaBodyOpening(content string) int {
	masked := sanitizeJava(content)
	parentheses := 0
	for i := range len(masked) {
		switch masked[i] {
		case '(':
			parentheses++
		case ')':
			parentheses--
		case '{':
			if parentheses == 0 {
				return i
			}
		}
	}
	return -1
}

func isJavaInvocationKeyword(name string) bool {
	return slices.Contains([]string{"catch", "for", "if", "new", "return", "super", "switch", "synchronized", "this", "throw", "while"}, name)
}
