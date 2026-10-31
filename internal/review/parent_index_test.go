package review

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func legacyGraphParents(nodes []GraphNode) map[string]string {
	result := map[string]string{}
	for _, node := range nodes {
		var parent *GraphNode
		for _, candidate := range nodes {
			if candidate.ID != node.ID && candidate.Kind != 4 && candidate.Path == node.Path && candidate.Revision == node.Revision && candidate.StartLine <= node.StartLine && candidate.EndLine >= node.EndLine && (parent == nil || candidate.EndLine-candidate.StartLine < parent.EndLine-parent.StartLine) {
				copy := candidate
				parent = &copy
			}
		}
		if parent != nil {
			result[node.ID] = parent.ID
		}
	}
	return result
}

func TestParentIndexMatchesLegacyContainmentAndTies(t *testing.T) {
	random := rand.New(rand.NewSource(8703))
	for fixture := 0; fixture < 50; fixture++ {
		nodes := []GraphNode{}
		for i := 0; i < 80; i++ {
			start := 1 + random.Intn(30)
			nodes = append(nodes, GraphNode{ID: fmt.Sprintf("n%d", i), Path: fmt.Sprintf("%d.java", i%2), Revision: Revision(i % 3 % 2), Kind: random.Intn(5), StartLine: start, EndLine: start + random.Intn(12)})
		}
		got, _ := graphParents(nodes)
		if want := legacyGraphParents(nodes); !reflect.DeepEqual(got, want) {
			t.Fatalf("fixture %d changed parent identity or equal-span order", fixture)
		}
	}
	nodes := []GraphNode{{ID: "first", Path: "A.java", Kind: 0, StartLine: 1, EndLine: 2}, {ID: "second", Path: "A.java", Kind: 1, StartLine: 1, EndLine: 2}, {ID: "method", Path: "A.java", Kind: 4, StartLine: 1, EndLine: 2}}
	got, _ := graphParents(nodes)
	if got["first"] != "second" || got["second"] != "first" || got["method"] != "first" {
		t.Fatalf("same-line declarations changed self exclusion or tie order: %v", got)
	}
}

func TestParentIndexHasBoundedWorkInOneLargeFile(t *testing.T) {
	for _, size := range []int{128, 256, 512, 1024} {
		nodes := make([]GraphNode, size)
		for i := range nodes {
			nodes[i] = GraphNode{ID: fmt.Sprintf("n%d", i), Path: "A.java", Kind: i % 5, StartLine: i + 1, EndLine: size*2 - i}
		}
		_, stats := graphParents(nodes)
		levels := 1
		for width := 1; width < size; width *= 2 {
			levels++
		}
		if stats.Queries != size || stats.InsertSteps > size*levels || stats.QuerySegments > size*levels*2 {
			t.Fatalf("%d nodes used more than logarithmic parent work: %+v", size, stats)
		}
	}
}
