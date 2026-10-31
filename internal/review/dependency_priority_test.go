package review

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDependencyPriorityIndexPreservesFirstMatchAndOrdering(t *testing.T) {
	graph := Graph{Nodes: []GraphNode{{Path: "A.java", Revision: Head, StartLine: 1, EndLine: 3, Name: "plain", Change: 0}, {Path: "A.java", Revision: Head, StartLine: 1, EndLine: 3, Name: "security", Change: 2}, {Path: "B.java", Revision: Base, StartLine: 1, EndLine: 3, Name: "AuthBoundary", Change: 2}}}
	index, err := newDependencyPriorityIndexContext(context.Background(), graph)
	if err != nil {
		t.Fatal(err)
	}
	if priority := index.priority(CodeSlice{Path: "A.java", Revision: Head, StartLine: 1, EndLine: 3}); priority != 2 {
		t.Fatalf("later matching node replaced first priority: %d", priority)
	}
	if priority := index.priority(CodeSlice{Path: "B.java", Revision: Base, StartLine: 1, EndLine: 3}); priority != 0 {
		t.Fatalf("named boundary priority changed: %d", priority)
	}
	additions := []dependencyAddition{}
	for i := 0; i < 48; i++ {
		path := []string{"A.java", "B.java", "Plain.java", "Gateway.java"}[i%4]
		additions = append(additions, dependencyAddition{owner: i, source: CodeSlice{Path: path, Revision: Revision((i / 4) % 2), StartLine: 1, EndLine: 3, Content: strings.Repeat("x", i%5)}})
	}
	want := append([]dependencyAddition{}, additions...)
	slices.SortStableFunc(want, func(a, b dependencyAddition) int {
		a.priority = dependencyPriority(graph, a.source)
		b.priority = dependencyPriority(graph, b.source)
		return compareDependencyAdditions(a, b)
	})
	if err := sortDependencyAdditionsContext(context.Background(), graph, additions); err != nil {
		t.Fatal(err)
	}
	for i := range additions {
		want[i].priority = dependencyPriority(graph, want[i].source)
	}
	if !reflect.DeepEqual(additions, want) {
		t.Fatal("indexed priority changed first-match or stable ordering")
	}
}
func TestDependencyPriorityScanObservesCancellation(t *testing.T) {
	graph := Graph{Nodes: make([]GraphNode, 2048)}
	ctx := newReviewCountingContext(4)
	defer ctx.cancel()
	if _, err := newDependencyPriorityIndexContext(ctx, graph); !errors.Is(err, context.Canceled) {
		t.Fatalf("priority scan ignored cancellation: %v polls%d", err, ctx.calls)
	}
}
func TestDependencyStableSortStopsOnCancellation(t *testing.T) {
	values := make([]int, 10000)
	for i := range values {
		values[i] = len(values) - i
	}
	ctx := newReviewCountingContext(3)
	defer ctx.cancel()
	if err := sortStableContext(ctx, values, func(a, b int) int { return a - b }); !errors.Is(err, context.Canceled) {
		t.Fatalf("stable sort ignored cancellation: %v polls%d", err, ctx.calls)
	}
}
