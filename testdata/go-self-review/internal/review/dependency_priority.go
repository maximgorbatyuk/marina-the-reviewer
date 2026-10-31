package review

import (
	"context"
	"strings"
)

type dependencyAddition struct {
	owner    int
	source   CodeSlice
	priority int
}
type dependencyPriorityIndex map[contextSliceKey]int

func newDependencyPriorityIndexContext(ctx context.Context, graph Graph) (dependencyPriorityIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	index := dependencyPriorityIndex{}
	poll := cpuContextPoll{context: ctx}
	for _, node := range graph.Nodes {
		if err := poll.check(); err != nil {
			return nil, err
		}
		key := contextSliceKey{node.Path, node.Revision, node.StartLine, node.EndLine}
		if _, exists := index[key]; exists {
			continue
		}
		priority := 1
		name := strings.ToLower(node.Name)
		for _, marker := range boundaryMarkers {
			if strings.Contains(name, marker) {
				priority = 0
				break
			}
		}
		if priority != 0 && node.Change == 0 {
			priority = 2
		}
		index[key] = priority
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return index, nil
}
func (index dependencyPriorityIndex) priority(source CodeSlice) int {
	path := strings.ToLower(source.Path)
	for _, marker := range boundaryMarkers {
		if strings.Contains(path, marker) {
			return 0
		}
	}
	if priority, found := index[contextKey(source)]; found {
		return priority
	}
	return 1
}
func compareDependencyAdditions(a, b dependencyAddition) int {
	if difference := a.priority - b.priority; difference != 0 {
		return difference
	}
	if len(a.source.Content) != len(b.source.Content) {
		return len(a.source.Content) - len(b.source.Content)
	}
	if difference := ordinalCompare(a.source.Path, b.source.Path); difference != 0 {
		return difference
	}
	if a.source.Revision != b.source.Revision {
		return int(a.source.Revision - b.source.Revision)
	}
	return a.source.StartLine - b.source.StartLine
}
func sortDependencyAdditionsContext(ctx context.Context, graph Graph, additions []dependencyAddition) error {
	index, err := newDependencyPriorityIndexContext(ctx, graph)
	if err != nil {
		return err
	}
	for i := range additions {
		if err = ctx.Err(); err != nil {
			return err
		}
		additions[i].priority = index.priority(additions[i].source)
	}
	return sortStableContext(ctx, additions, compareDependencyAdditions)
}
