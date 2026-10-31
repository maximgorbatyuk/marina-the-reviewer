package review

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"
)

func legacyMarkUnchangedCalls(g *Graph) int {
	byID := map[string]GraphNode{}
	for _, node := range g.Nodes {
		byID[node.ID] = node
	}
	visits := 0
	for i, edge := range g.Edges {
		if edge.Kind != "call" {
			continue
		}
		node := byID[edge.SourceID]
		if node.CounterpartID == nil {
			continue
		}
		targets := []string{}
		for _, id := range edge.TargetIDs {
			target := byID[id]
			if target.CounterpartID == nil {
				targets = append(targets, "?")
			} else {
				targets = append(targets, *target.CounterpartID)
			}
		}
		slices.SortFunc(targets, ordinalCompare)
		for _, other := range g.Edges {
			visits++
			otherTargets := append([]string{}, other.TargetIDs...)
			slices.SortFunc(otherTargets, ordinalCompare)
			if other.Kind == "call" && other.SourceID == *node.CounterpartID && canonicalJSON(edge.InvokedName) == canonicalJSON(other.InvokedName) && canonicalJSON(edge.ReceiverType) == canonicalJSON(other.ReceiverType) && edge.Resolution == other.Resolution && slices.Equal(targets, otherTargets) {
				g.Edges[i].Change = "unchanged"
				break
			}
		}
	}
	return visits
}

func TestCallIndexMatchesLegacyComparison(t *testing.T) {
	values := []*string{nil, callIndexString(""), callIndexString("f"), callIndexString("a|b\x00c")}
	random := rand.New(rand.NewSource(1907))
	for fixture := 0; fixture < 30; fixture++ {
		g := Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Limitations: []string{}}
		for i := 0; i < 8; i++ {
			id, counterpart := fmt.Sprintf("n%d", i), fmt.Sprintf("n%d", i^1)
			node := GraphNode{ID: id}
			if i < 6 {
				node.CounterpartID = &counterpart
			}
			g.Nodes = append(g.Nodes, node)
		}
		for i := 0; i < 80; i++ {
			targets := []string{}
			for j := 0; j < random.Intn(7); j++ {
				targets = append(targets, fmt.Sprintf("n%d", random.Intn(10)))
			}
			if i%9 == 0 {
				targets = nil
			}
			kind := "call"
			if i%7 == 0 {
				kind = "contains"
			}
			line := i
			edge := GraphEdge{ID: fmt.Sprintf("e%d", i), Kind: kind, SourceID: fmt.Sprintf("n%d", random.Intn(10)), TargetIDs: targets, Revision: Revision(i % 2), Line: &line, InvokedName: values[random.Intn(len(values))], ReceiverType: values[random.Intn(len(values))], Resolution: random.Intn(3), Change: "added"}
			g.Edges = append(g.Edges, edge)
			if i%5 == 0 {
				other := edge
				other.ID += "-counterpart"
				other.SourceID = fmt.Sprintf("n%d", i%6^1)
				g.Edges[len(g.Edges)-1].SourceID = fmt.Sprintf("n%d", i%6)
				other.TargetIDs = []string{}
				for _, target := range targets {
					for _, node := range g.Nodes {
						if node.ID == target && node.CounterpartID != nil {
							other.TargetIDs = append(other.TargetIDs, *node.CounterpartID)
						}
					}
				}
				slices.Reverse(other.TargetIDs)
				g.Edges = append(g.Edges, other, other)
			}
		}
		before := canonicalJSON(g)
		want := g
		want.Edges = append([]GraphEdge{}, g.Edges...)
		legacyMarkUnchangedCalls(&want)
		got := g
		got.Edges = append([]GraphEdge{}, g.Edges...)
		markUnchangedCalls(&got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("fixture %d differs from the legacy comparison", fixture)
		}
		if canonicalJSON(g) != before {
			t.Fatalf("fixture %d changed shared target order", fixture)
		}
	}
}

func TestCallIndexPreservesTargetMultiplicityAndNullableFields(t *testing.T) {
	for _, tc := range []struct {
		name            string
		left, right     []string
		invoked, other  *string
		receiver, owner *string
		want            string
	}{
		{"same multiset", []string{"t0", "t0", "t2"}, []string{"t3", "t1", "t1"}, nil, nil, nil, nil, "unchanged"},
		{"target multiplicity", []string{"t0", "t0", "t2"}, []string{"t3", "t1"}, nil, nil, nil, nil, "added"},
		{"empty targets", nil, []string{}, nil, nil, nil, nil, "unchanged"},
		{"null invoked name", nil, nil, nil, callIndexString(""), nil, nil, "added"},
		{"null receiver", nil, nil, nil, nil, nil, callIndexString(""), "added"},
		{"missing target counterpart", []string{"missing"}, []string{"?"}, nil, nil, nil, nil, "unchanged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := Graph{Nodes: []GraphNode{{ID: "source", CounterpartID: callIndexString("other")}, {ID: "other"}, {ID: "t0", CounterpartID: callIndexString("t1")}, {ID: "t2", CounterpartID: callIndexString("t3")}}, Edges: []GraphEdge{
				{ID: "e1", Kind: "call", SourceID: "source", TargetIDs: tc.left, InvokedName: tc.invoked, ReceiverType: tc.receiver, Change: "added"},
				{ID: "e2", Kind: "call", SourceID: "other", TargetIDs: tc.right, InvokedName: tc.other, ReceiverType: tc.owner, Revision: Head, Change: "removed"},
			}}
			markUnchangedCalls(&g)
			if g.Edges[0].Change != tc.want || g.Edges[1].Change != "removed" {
				t.Fatalf("comparison changed: %+v", g.Edges)
			}
		})
	}
}

func TestCallIndexWorkScalesWithEdges(t *testing.T) {
	for _, count := range []int{128, 256, 512} {
		g := callIndexScaleGraph(count)
		stats := markUnchangedCalls(&g)
		if stats.IndexedCalls != count || stats.Lookups != count || stats.TargetElements != count*6 {
			t.Fatalf("%d calls used non-linear work: %+v", count, stats)
		}
	}
	g := callIndexScaleGraph(128)
	if visits := legacyMarkUnchangedCalls(&g); visits != 128*128 {
		t.Fatalf("the bounded fixture no longer exposes the old quadratic scan: %d", visits)
	}
}

func BenchmarkCallIndex(b *testing.B) {
	for _, count := range []int{256, 512, 1024, 2048} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			graph := callIndexScaleGraph(count)
			b.ReportAllocs()
			b.ReportMetric(float64(count), "calls/op")
			for b.Loop() {
				g := graph
				g.Edges = append([]GraphEdge{}, graph.Edges...)
				markUnchangedCalls(&g)
			}
		})
	}
}

func callIndexScaleGraph(count int) Graph {
	g := Graph{Nodes: []GraphNode{{ID: "source", CounterpartID: callIndexString("other")}, {ID: "other"}, {ID: "t", CounterpartID: callIndexString("t-counterpart")}}, Edges: make([]GraphEdge, count)}
	for i := range g.Edges {
		g.Edges[i] = GraphEdge{ID: fmt.Sprintf("e%d", i), Kind: "call", SourceID: "source", TargetIDs: []string{"t", "t", "missing"}, InvokedName: callIndexString(fmt.Sprintf("f%d", i)), Change: "added"}
	}
	return g
}

func callIndexString(value string) *string { return &value }
