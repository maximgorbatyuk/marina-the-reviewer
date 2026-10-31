package review

import (
	"context"
	"slices"
)

type projectionPath struct {
	path     string
	revision Revision
}
type projectionRange struct{ start, end int }
type projectionMethod struct {
	path        string
	revision    Revision
	owner, name string
}
type projectionSelection struct {
	membership           []byte
	nodeCount, edgeCount int
	edges                []GraphEdge
}
type projectionCallTarget struct {
	ordinal                            int
	method, nameMatch, contractChanged bool
	methods                            []int
}
type projectionMethodTarget struct {
	ordinal int
	name    string
}
type projectionEdge struct {
	source  int
	targets []int
	calls   []projectionCallTarget
}
type graphProjector struct {
	full                                       Graph
	ordinals                                   map[string]int
	bySymbol                                   map[string][]int
	methods                                    map[projectionMethod][]int
	limitations                                []string
	methodRanges                               map[projectionPath][]projectionRange
	mandatory                                  []byte
	mandatoryCount                             int
	changed                                    int
	visible                                    []uint64
	generation                                 uint64
	primary                                    map[projectionPath][]projectionRange
	queue                                      []int
	nodeMaterializations, edgeMaterializations int
	methodCandidateScans                       int
	edgeIndices                                []projectionEdge
	counterpartIndices                         []int
	empty                                      GraphNode
}

func newGraphProjector(full Graph) *graphProjector {
	p, _ := newGraphProjectorContext(context.Background(), full)
	return p
}
func newGraphProjectorContext(ctx context.Context, full Graph) (*graphProjector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	poll := cpuContextPoll{context: ctx}

	p := &graphProjector{full: full, ordinals: map[string]int{}, bySymbol: map[string][]int{}, methods: map[projectionMethod][]int{}, methodRanges: map[projectionPath][]projectionRange{}, mandatory: make([]byte, (len(full.Nodes)+7)/8), visible: make([]uint64, len(full.Nodes)), primary: map[projectionPath][]projectionRange{}}
	for i, n := range full.Nodes {
		if err := poll.check(); err != nil {
			return nil, err
		}
		p.ordinals[n.ID] = i
		p.bySymbol[n.ID] = append(p.bySymbol[n.ID], i)
		if n.SourceSymbolID != n.ID {
			p.bySymbol[n.SourceSymbolID] = append(p.bySymbol[n.SourceSymbolID], i)
		}
		if n.Change != 0 {
			p.changed++
			p.mandatory[i/8] |= 1 << uint(i%8)
		}
		if n.Kind == 4 {
			key := projectionPath{n.Path, n.Revision}
			p.methodRanges[key] = append(p.methodRanges[key], projectionRange{n.StartLine, n.EndLine})
			if n.DeclaringType != nil {
				m := projectionMethod{n.Path, n.Revision, *n.DeclaringType, n.Name}
				p.methods[m] = append(p.methods[m], i)
			}
		}
	}
	for _, node := range full.Nodes {
		if err := poll.check(); err != nil {
			return nil, err
		}
		other := -1
		if node.CounterpartID != nil {
			if index, ok := p.ordinals[*node.CounterpartID]; ok {
				other = index
			}
		}
		p.counterpartIndices = append(p.counterpartIndices, other)
	}
	methodSets := map[projectionMethodTarget][]int{}
	for _, edge := range full.Edges {
		if err := poll.check(); err != nil {
			return nil, err
		}
		source := -1
		if index, ok := p.ordinals[edge.SourceID]; ok {
			source = index
		}
		entry := projectionEdge{source: source}
		for _, id := range edge.TargetIDs {
			if err := poll.check(); err != nil {
				return nil, err
			}
			target := -1
			if index, ok := p.ordinals[id]; ok {
				target = index
			}
			entry.targets = append(entry.targets, target)
			if edge.Kind != "call" || target < 0 || source < 0 || full.Nodes[source].Change == 0 {
				continue
			}
			node := &full.Nodes[target]
			call := projectionCallTarget{ordinal: target, method: node.Kind == 4, nameMatch: edge.InvokedName != nil && *edge.InvokedName == node.Name, contractChanged: node.Change == 1 || node.Change == 3}
			if node.CounterpartID != nil {
				otherSignature := ""
				if other := p.counterpartIndices[target]; other >= 0 {
					otherSignature = full.Nodes[other].Signature
				}
				if node.Signature != otherSignature {
					call.contractChanged = true
				}
			}
			if node.Kind != 4 && edge.InvokedName != nil {
				key := projectionMethodTarget{target, *edge.InvokedName}
				methods, found := methodSets[key]
				if !found {
					for _, index := range p.methods[projectionMethod{node.Path, node.Revision, node.Name, *edge.InvokedName}] {
						if err := poll.check(); err != nil {
							return nil, err
						}
						p.methodCandidateScans++
						method := full.Nodes[index]
						if method.StartLine >= node.StartLine && method.EndLine <= node.EndLine {
							methods = append(methods, index)
						}
					}
					methodSets[key] = methods
				}
				call.methods = methods
			}
			entry.calls = append(entry.calls, call)
		}
		p.edgeIndices = append(p.edgeIndices, entry)
	}
	for key, ranges := range p.methodRanges {
		if err := poll.check(); err != nil {
			return nil, err
		}
		p.methodRanges[key] = projectionPrefixRanges(ranges)
	}
	for i, n := range full.Nodes {
		if err := poll.check(); err != nil {
			return nil, err
		}
		if n.Change != 0 {
			p.queue = append(p.queue, i)
			p.mandatoryCount++
		}
	}
	for at := 0; at < len(p.queue); at++ {
		if err := poll.check(); err != nil {
			return nil, err
		}
		n := full.Nodes[p.queue[at]]
		if n.CounterpartID != nil {
			if i, ok := p.ordinals[*n.CounterpartID]; ok && p.mandatory[i/8]&(1<<uint(i%8)) == 0 {
				p.mandatory[i/8] |= 1 << uint(i%8)
				p.mandatoryCount++
				p.queue = append(p.queue, i)
			}
		}
	}
	p.queue = p.queue[:0]
	p.limitations = unique(append(append([]string{}, full.Limitations...), "All changed MR symbols are listed; only relations relevant to this batch are included. Omitted relations do not prove absence.", "Incoming calls from unchanged callers are omitted. Calls from supplied primary source lines are retained even when their source symbol is unchanged. The full graph remains in the review artifact."))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *graphProjector) nodes(selected projectionSelection) []GraphNode {
	nodes, _ := p.nodesContext(context.Background(), selected)
	return nodes
}
func (p *graphProjector) nodesContext(ctx context.Context, selected projectionSelection) ([]GraphNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	poll := cpuContextPoll{context: ctx}

	p.nodeMaterializations++
	if selected.nodeCount == len(p.full.Nodes) && len(p.full.Nodes) > 0 {
		return p.full.Nodes, nil
	}
	nodes := make([]GraphNode, 0, selected.nodeCount)
	for i, n := range p.full.Nodes {
		if err := poll.check(); err != nil {
			return nil, err
		}
		if selected.membership[i/8]&(1<<uint(i%8)) != 0 {
			nodes = append(nodes, n)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nodes, nil
}
func (p *graphProjector) project(batch Batch) GraphProjection {
	selected := p.selectGraph(batch, nil)
	return GraphProjection{p.nodes(selected), selected.edges, p.full.Sha256, "", p.changed, len(p.full.Nodes), len(p.full.Edges), p.limitations, selected.nodeCount == len(p.full.Nodes) && selected.edgeCount == len(p.full.Edges)}
}
func (p *graphProjector) selectGraph(batch Batch, visit func(int)) projectionSelection {
	selected, _ := p.selectGraphContext(context.Background(), batch, visit)
	return selected
}
func (p *graphProjector) selectGraphContext(ctx context.Context, batch Batch, visit func(int)) (projectionSelection, error) {
	if err := ctx.Err(); err != nil {
		return projectionSelection{}, err
	}
	poll := cpuContextPoll{context: ctx}

	p.generation++
	if p.generation == 0 {
		clear(p.visible)
		p.generation = 1
	}
	generation := p.generation
	p.queue = p.queue[:0]
	selected := projectionSelection{membership: slices.Clone(p.mandatory), nodeCount: p.mandatoryCount, edges: []GraphEdge{}}
	include := func(index int) {
		if index >= 0 && selected.membership[index/8]&(1<<uint(index%8)) == 0 {
			selected.membership[index/8] |= 1 << uint(index%8)
			selected.nodeCount++
			p.queue = append(p.queue, index)
		}
	}
	visible := func(index int) bool { return index >= 0 && p.visible[index] == generation }
	for key, ranges := range p.primary {
		if err := poll.check(); err != nil {
			return projectionSelection{}, err
		}
		p.primary[key] = ranges[:0]
	}
	for _, b := range batch.Blocks {
		if err := poll.check(); err != nil {
			return projectionSelection{}, err
		}
		for _, source := range []*CodeSlice{b.Base, b.Head} {
			if err := poll.check(); err != nil {
				return projectionSelection{}, err
			}
			if source != nil {
				key := projectionPath{source.Path, source.Revision}
				p.primary[key] = append(p.primary[key], projectionRange{source.StartLine, source.EndLine})
			}
		}
		for _, id := range b.SymbolIDs {
			if err := poll.check(); err != nil {
				return projectionSelection{}, err
			}
			for _, i := range p.bySymbol[id] {
				if err := poll.check(); err != nil {
					return projectionSelection{}, err
				}
				n := p.full.Nodes[i]
				for _, source := range []*CodeSlice{b.Base, b.Head} {
					if err := poll.check(); err != nil {
						return projectionSelection{}, err
					}
					if !intersects(n, source) {
						continue
					}
					yes := n.Kind == 4 || source.IsComplete || n.StartLine >= source.StartLine
					if !yes {
						for _, ch := range b.ChangedLines {
							if err := poll.check(); err != nil {
								return projectionSelection{}, err
							}
							if ch.Revision == n.Revision && ch.Line >= source.StartLine && ch.Line <= source.EndLine && !projectionContainsLine(p.methodRanges[projectionPath{n.Path, n.Revision}], ch.Line) {
								yes = true
							}
						}
					}
					if yes {
						p.visible[i] = generation
						include(i)
					}
				}
			}
		}
	}
	for key, ranges := range p.primary {
		if err := poll.check(); err != nil {
			return projectionSelection{}, err
		}
		p.primary[key] = projectionPrefixRanges(ranges)
	}
	contains := func(n GraphNode, line int) bool {
		return projectionContainsLine(p.primary[projectionPath{n.Path, n.Revision}], line)
	}
	for edgeIndex := range p.full.Edges {
		if err := poll.check(); err != nil {
			return projectionSelection{}, err
		}
		edge := &p.full.Edges[edgeIndex]
		entry := &p.edgeIndices[edgeIndex]
		source := &p.empty
		if entry.source >= 0 {
			source = &p.full.Nodes[entry.source]
		}
		keep := false
		switch edge.Kind {
		case "call":
			if source.Change != 0 {
				for _, call := range entry.calls {
					if err := poll.check(); err != nil {
						return projectionSelection{}, err
					}
					target := &p.full.Nodes[call.ordinal]
					if call.method {
						keep = keep || visible(call.ordinal)
						continue
					}
					for _, method := range call.methods {
						if err := poll.check(); err != nil {
							return projectionSelection{}, err
						}
						keep = keep || visible(method)
					}
					if visible(call.ordinal) {
						if call.nameMatch {
							keep = true
						}
						if call.contractChanged && contains(*target, target.StartLine) {
							keep = true
						}
					}
				}
			}
			if edge.Line != nil && contains(*source, *edge.Line) {
				keep = true
			}
		case "contains":
			for _, target := range entry.targets {
				if err := poll.check(); err != nil {
					return projectionSelection{}, err
				}
				keep = keep || visible(target)
			}
		default:
			keep = visible(entry.source)
			for _, target := range entry.targets {
				if err := poll.check(); err != nil {
					return projectionSelection{}, err
				}
				keep = keep || visible(target)
			}
		}
		if keep {
			selected.edgeCount++
			if visit == nil {
				selected.edges = append(selected.edges, *edge)
				p.edgeMaterializations++
			} else {
				visit(edgeIndex)
			}
			include(entry.source)
			for _, target := range entry.targets {
				if err := poll.check(); err != nil {
					return projectionSelection{}, err
				}
				include(target)
			}
		}
	}
	for at := 0; at < len(p.queue); at++ {
		if err := poll.check(); err != nil {
			return projectionSelection{}, err
		}
		include(p.counterpartIndices[p.queue[at]])
	}
	p.queue = p.queue[:0]
	if err := ctx.Err(); err != nil {
		return projectionSelection{}, err
	}
	return selected, nil
}
func projectionPrefixRanges(ranges []projectionRange) []projectionRange {
	slices.SortFunc(ranges, func(a, b projectionRange) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.end - b.end
	})
	for i := 1; i < len(ranges); i++ {
		ranges[i].end = max(ranges[i].end, ranges[i-1].end)
	}
	return ranges
}
func projectionContainsLine(ranges []projectionRange, line int) bool {
	index, _ := slices.BinarySearchFunc(ranges, line, func(r projectionRange, line int) int {
		if r.start <= line {
			return -1
		}
		return 1
	})
	return index > 0 && ranges[index-1].end >= line
}
