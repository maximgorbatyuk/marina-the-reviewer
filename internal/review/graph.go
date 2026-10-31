package review

import (
	"context"
	"slices"
	"strings"
)

type GraphProjection struct {
	Nodes            []GraphNode `json:"nodes"`
	Edges            []GraphEdge `json:"edges"`
	FullGraphSha256  string      `json:"fullGraphSha256"`
	Sha256           string      `json:"sha256"`
	ChangedNodeCount int         `json:"changedNodeCount"`
	FullNodeCount    int         `json:"fullNodeCount"`
	FullEdgeCount    int         `json:"fullEdgeCount"`
	Limitations      []string    `json:"limitations"`
	IsCompleteGraph  bool        `json:"isCompleteGraph"`
}

func intersects(n GraphNode, s *CodeSlice) bool {
	return s != nil && n.Path == s.Path && n.Revision == s.Revision && n.StartLine <= s.EndLine && n.EndLine >= s.StartLine
}
func containsLine(n GraphNode, line int, s *CodeSlice) bool {
	return s != nil && n.Path == s.Path && n.Revision == s.Revision && line >= s.StartLine && line <= s.EndLine
}
func ProjectGraph(full Graph, batch Batch) GraphProjection {
	all := map[string]GraphNode{}
	changed, visible, included := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, n := range full.Nodes {
		all[n.ID] = n
		if n.Change != 0 {
			changed[n.ID] = true
			included[n.ID] = true
		}
	}
	for _, n := range full.Nodes {
		for _, b := range batch.Blocks {
			if !slices.Contains(b.SymbolIDs, n.ID) && !slices.Contains(b.SymbolIDs, n.SourceSymbolID) {
				continue
			}
			for _, s := range []*CodeSlice{b.Base, b.Head} {
				if !intersects(n, s) {
					continue
				}
				isVisible := n.Kind == 4 || s.IsComplete || n.StartLine >= s.StartLine
				if !isVisible {
					for _, ch := range b.ChangedLines {
						if ch.Revision == n.Revision && ch.Line >= s.StartLine && ch.Line <= s.EndLine && !slices.ContainsFunc(full.Nodes, func(m GraphNode) bool {
							return m.Kind == 4 && m.Path == n.Path && m.Revision == n.Revision && m.StartLine <= ch.Line && m.EndLine >= ch.Line
						}) {
							isVisible = true
						}
					}
				}
				if isVisible {
					visible[n.ID] = true
					included[n.ID] = true
				}
			}
		}
	}
	edges := []GraphEdge{}
	for _, e := range full.Edges {
		keep := false
		source := all[e.SourceID]
		switch e.Kind {
		case "call":
			if source.Change != 0 {
				for _, targetID := range e.TargetIDs {
					target, ok := all[targetID]
					if !ok {
						continue
					}
					if target.Kind == 4 {
						keep = keep || visible[targetID]
						continue
					}
					for id := range visible {
						m := all[id]
						if m.Kind == 4 && m.Revision == target.Revision && m.Path == target.Path && m.DeclaringType != nil && *m.DeclaringType == target.Name && e.InvokedName != nil && m.Name == *e.InvokedName && m.StartLine >= target.StartLine && m.EndLine <= target.EndLine {
							keep = true
						}
					}
					if visible[targetID] {
						if e.InvokedName != nil && *e.InvokedName == target.Name {
							keep = true
						}
						contractChanged := target.Change == 1 || target.Change == 3
						if target.CounterpartID != nil && target.Signature != all[*target.CounterpartID].Signature {
							contractChanged = true
						}
						if contractChanged {
							for _, b := range batch.Blocks {
								keep = keep || containsLine(target, target.StartLine, b.Base) || containsLine(target, target.StartLine, b.Head)
							}
						}
					}
				}
			}
			if e.Line != nil {
				for _, b := range batch.Blocks {
					keep = keep || containsLine(source, *e.Line, b.Base) || containsLine(source, *e.Line, b.Head)
				}
			}
		case "contains":
			for _, id := range e.TargetIDs {
				keep = keep || visible[id]
			}
		default:
			keep = visible[e.SourceID]
			for _, id := range e.TargetIDs {
				keep = keep || visible[id]
			}
		}
		if keep {
			edges = append(edges, e)
			included[e.SourceID] = true
			for _, id := range e.TargetIDs {
				included[id] = true
			}
		}
	}
	for expand := true; expand; {
		expand = false
		for id := range included {
			n, ok := all[id]
			if ok && n.CounterpartID != nil && !included[*n.CounterpartID] {
				included[*n.CounterpartID] = true
				expand = true
			}
		}
	}
	nodes := []GraphNode{}
	for _, n := range full.Nodes {
		if included[n.ID] {
			nodes = append(nodes, n)
		}
	}
	limitations := unique(append(append([]string{}, full.Limitations...), "All changed MR symbols are listed; only relations relevant to this batch are included. Omitted relations do not prove absence.", "Incoming calls from unchanged callers are omitted. Calls from supplied primary source lines are retained even when their source symbol is unchanged. The full graph remains in the review artifact."))
	hash := Hash(defaultJSON(struct {
		Sha256      string
		Nodes       []GraphNode `json:"nodes"`
		Edges       []GraphEdge `json:"edges"`
		Limitations []string    `json:"limitations"`
	}{full.Sha256, nodes, edges, limitations}))
	return GraphProjection{nodes, edges, full.Sha256, hash, len(changed), len(full.Nodes), len(full.Edges), limitations, len(nodes) == len(full.Nodes) && len(edges) == len(full.Edges)}
}
func commonPath(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	prefix := paths[0]
	for _, p := range paths[1:] {
		i := 0
		for i < len(prefix) && i < len(p) && prefix[i] == p[i] {
			i++
		}
		prefix = prefix[:i]
	}
	i := strings.LastIndex(prefix, "/")
	if i < 0 {
		return ""
	}
	return prefix[:i+1]
}
func pathPrefixes(paths []string) []string {
	common := commonPath(paths)
	keys := []string{}
	groups := map[string][]string{}
	for _, p := range paths {
		key := common
		if i := strings.Index(p[len(common):], "/"); i >= 0 {
			key = p[:len(common)+i+1]
		}
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], p)
	}
	out := []string{}
	for _, k := range keys {
		out = append(out, commonPath(groups[k]))
	}
	out = unique(out)
	slices.SortFunc(out, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return ordinalCompare(a, b)
	})
	return out
}
func shared(before, after any) any {
	if before == nil {
		return after
	}
	if after == nil || canonicalJSON(before) == canonicalJSON(after) {
		return before
	}
	return []any{before, after}
}
func ProjectGraphContext(ctx context.Context, full Graph, batch Batch) (GraphProjection, error) {
	projector, err := newGraphProjectorContext(ctx, full)
	if err != nil {
		return GraphProjection{}, err
	}
	selected, err := projector.selectGraphContext(ctx, batch, nil)
	if err != nil {
		return GraphProjection{}, err
	}
	nodes, err := projector.nodesContext(ctx, selected)
	if err != nil {
		return GraphProjection{}, err
	}
	graph := GraphProjection{nodes, selected.edges, full.Sha256, "", projector.changed, len(full.Nodes), len(full.Edges), projector.limitations, selected.nodeCount == len(full.Nodes) && selected.edgeCount == len(full.Edges)}
	graph.Sha256 = Hash(defaultJSON(struct {
		Sha256      string
		Nodes       []GraphNode `json:"nodes"`
		Edges       []GraphEdge `json:"edges"`
		Limitations []string    `json:"limitations"`
	}{full.Sha256, nodes, selected.edges, projector.limitations}))
	if err = ctx.Err(); err != nil {
		return GraphProjection{}, err
	}
	return graph, nil
}
func SerializeInput(input BatchInput) string {
	wire, _ := serializeProjectedInput(context.Background(), input, ProjectGraph(input.Graph, input.Batch))
	return wire
}
func SerializeInputContext(ctx context.Context, input BatchInput) (string, error) {
	graph, err := ProjectGraphContext(ctx, input.Graph, input.Batch)
	if err != nil {
		return "", err
	}
	return serializeProjectedInput(ctx, input, graph)
}
func serializeProjectedInput(ctx context.Context, input BatchInput, g GraphProjection) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	poll := cpuContextPoll{context: ctx}

	all := map[string]GraphNode{}
	aliases := map[string]string{}
	for _, n := range input.Graph.Nodes {
		if err := poll.check(); err != nil {
			return "", err
		}
		all[n.ID] = n
		aliases[n.SourceSymbolID] = n.ID
	}
	catalogue, err := makeWireNodeCatalogueContext(ctx, g.Nodes)
	if err != nil {
		return "", err
	}
	prefixes, wirePaths, owners, nodeGroups := catalogue.Prefixes, catalogue.Paths, catalogue.Owners, catalogue.Groups
	edgeGroups := []any{}
	edgeIndex := map[string]int{}
	for _, e := range g.Edges {
		if err := poll.check(); err != nil {
			return "", err
		}
		kind := map[string]int{"call": 0, "contains": 1, "counterpart": 2, "possible-move": 3}[e.Kind]
		change := map[string]int{"unchanged": 0, "added": 1, "removed": 2, "structural": 3, "matched-declaration": 4, "unproven": 5}[e.Change]
		head := []any{kind, e.TargetIDs, e.Revision, e.InvokedName, e.ReceiverType, e.Resolution, change, e.Confidence, e.Reasons}
		key := canonicalJSON(head)
		row := []any{e.ID, e.SourceID, e.Line}
		if i, ok := edgeIndex[key]; ok {
			prior := edgeGroups[i].([]any)
			prior[9] = append(prior[9].([]any), row)
		} else {
			edgeIndex[key] = len(edgeGroups)
			edgeGroups = append(edgeGroups, append(head, []any{row}))
		}
	}
	blocks := []wireBlock{}
	for _, b := range input.Batch.Blocks {
		if err := poll.check(); err != nil {
			return "", err
		}
		block, err := makeWireBlockContext(ctx, b, all, aliases)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, block)
	}
	limitations := []string{}
	for _, l := range input.Limitations {
		if err := poll.check(); err != nil {
			return "", err
		}
		if !slices.Contains(g.Limitations, l) {
			limitations = append(limitations, l)
		}
	}
	graph := struct {
		FullGraphSha256           string   `json:"fullGraphSha256"`
		Sha256                    string   `json:"sha256"`
		IsCompleteGraph           bool     `json:"isCompleteGraph"`
		AllChangedSymbolsIncluded bool     `json:"allChangedSymbolsIncluded"`
		ChangedNodeCount          int      `json:"changedNodeCount"`
		IncludedNodeCount         int      `json:"includedNodeCount"`
		IncludedEdgeCount         int      `json:"includedEdgeCount"`
		FullNodeCount             int      `json:"fullNodeCount"`
		FullEdgeCount             int      `json:"fullEdgeCount"`
		PathPrefixes              []string `json:"pathPrefixes"`
		Paths                     []any    `json:"paths"`
		Owners                    []string `json:"owners"`
		Legends                   any      `json:"legends"`
		NodeGroupColumns          []string `json:"nodeGroupColumns"`
		NodeGroups                []any    `json:"nodeGroups"`
		EdgeGroupColumns          []string `json:"edgeGroupColumns"`
		EdgeGroups                []any    `json:"edgeGroups"`
		Limitations               []string `json:"limitations"`
	}{g.FullGraphSha256, g.Sha256, g.IsCompleteGraph, true, g.ChangedNodeCount, len(g.Nodes), len(g.Edges), g.FullNodeCount, g.FullEdgeCount, prefixes, wirePaths, owners, struct {
		SymbolKind string `json:"symbolKind"`
		Change     string `json:"change"`
		EdgeKind   string `json:"edgeKind"`
		Revision   string `json:"revision"`
		Resolution string `json:"resolution"`
		EdgeChange string `json:"edgeChange"`
	}{"0=class,1=interface,2=record,3=enum,4=method", "0=unchanged,1=added,2=modified,3=deleted", "0=call,1=contains,2=counterpart,3=possible-move", "0=base,1=head", "0=resolved,1=ambiguous,2=unresolved", "0=unchanged,1=added,2=removed,3=structural,4=matched-declaration,5=unproven"}, []string{"pathIndex", "ownerIndex", "kind", "change", "nodes[ids[base,head],signature]"}, nodeGroups, []string{"kind", "targets", "revision", "invokedName", "receiverType", "resolution", "change", "confidence", "reasons", "instances[id,source,line]"}, edgeGroups, g.Limitations}
	wire := canonicalJSON(struct {
		Version     int      `json:"version"`
		Purpose     string   `json:"purpose"`
		BaseSha     string   `json:"baseSha"`
		HeadSha     string   `json:"headSha"`
		Graph       any      `json:"graph"`
		Batch       any      `json:"batch"`
		Limitations []string `json:"limitations"`
		Task        any      `json:"task"`
	}{input.Version, "ArchitectureReview", input.BaseSha, input.HeadSha, graph, struct {
		Index         int         `json:"index"`
		Count         int         `json:"count"`
		IsWholeReview bool        `json:"isWholeReview"`
		FileParts     []FilePart  `json:"fileParts"`
		Blocks        []wireBlock `json:"blocks"`
	}{input.Batch.Index, input.Batch.Count, input.Batch.IsWholeReview, input.Batch.FileParts, blocks}, limitations, input.Task})
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return wire, nil
}
