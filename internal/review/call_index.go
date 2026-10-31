package review

import (
	"context"
	"slices"
)

type callMatchStats struct {
	IndexedCalls   int
	Lookups        int
	TargetElements int
}

type optionalCallName struct {
	present bool
	value   string
}

type callMatchKey struct {
	source     string
	invoked    optionalCallName
	receiver   optionalCallName
	resolution int
	targets    string
}

func callName(value *string) optionalCallName {
	if value == nil {
		return optionalCallName{}
	}
	return optionalCallName{present: true, value: *value}
}

func canonicalCallTargets(targets []string) string {
	ordered := make([]string, len(targets))
	copy(ordered, targets)
	slices.SortFunc(ordered, ordinalCompare)
	return canonicalJSON(ordered)
}

func markUnchangedCalls(graph *Graph) callMatchStats {
	stats, _ := markUnchangedCallsContext(context.Background(), graph)
	return stats
}
func markUnchangedCallsContext(ctx context.Context, graph *Graph) (callMatchStats, error) {
	stats := callMatchStats{}
	nodes := make(map[string]GraphNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		nodes[node.ID] = node
	}
	index := make(map[callMatchKey]struct{})
	for _, edge := range graph.Edges {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if edge.Kind != "call" {
			continue
		}
		key := callMatchKey{edge.SourceID, callName(edge.InvokedName), callName(edge.ReceiverType), edge.Resolution, canonicalCallTargets(edge.TargetIDs)}
		index[key] = struct{}{}
		stats.IndexedCalls++
		stats.TargetElements += len(edge.TargetIDs)
	}
	for i, edge := range graph.Edges {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if edge.Kind != "call" {
			continue
		}
		node := nodes[edge.SourceID]
		if node.CounterpartID == nil {
			continue
		}
		targets := make([]string, len(edge.TargetIDs))
		for j, id := range edge.TargetIDs {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			targets[j] = "?"
			if counterpart := nodes[id].CounterpartID; counterpart != nil {
				targets[j] = *counterpart
			}
		}
		key := callMatchKey{*node.CounterpartID, callName(edge.InvokedName), callName(edge.ReceiverType), edge.Resolution, canonicalCallTargets(targets)}
		if _, found := index[key]; found {
			graph.Edges[i].Change = "unchanged"
		}
		stats.Lookups++
		stats.TargetElements += len(targets)
	}
	return stats, ctx.Err()
}
