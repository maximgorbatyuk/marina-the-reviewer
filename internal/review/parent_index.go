package review

import (
	"context"
	"slices"
)

type parentIndexStats struct{ Queries, InsertSteps, QuerySegments int }
type parentCandidates struct{ first, second int }

func graphParents(nodes []GraphNode) (map[string]string, parentIndexStats) {
	result, stats, _ := graphParentsContext(context.Background(), nodes)
	return result, stats
}
func graphParentsContext(ctx context.Context, nodes []GraphNode) (map[string]string, parentIndexStats, error) {
	type sourceKey struct {
		path     string
		revision Revision
	}
	result, stats := map[string]string{}, parentIndexStats{}
	groups := map[sourceKey][]int{}
	for i, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		key := sourceKey{node.Path, node.Revision}
		groups[key] = append(groups[key], i)
	}
	better := func(a, b int) bool {
		if a < 0 {
			return false
		}
		if b < 0 {
			return true
		}
		spanA, spanB := nodes[a].EndLine-nodes[a].StartLine, nodes[b].EndLine-nodes[b].StartLine
		return spanA < spanB || spanA == spanB && a < b
	}
	merge := func(a, b parentCandidates) parentCandidates {
		best := parentCandidates{-1, -1}
		for _, candidate := range []int{a.first, a.second, b.first, b.second} {
			if candidate < 0 || candidate == best.first || candidate == best.second {
				continue
			}
			if better(candidate, best.first) {
				best.second, best.first = best.first, candidate
			} else if better(candidate, best.second) {
				best.second = candidate
			}
		}
		return best
	}
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		candidates, ends := []int{}, []int{}
		for _, index := range group {
			if err := ctx.Err(); err != nil {
				return nil, stats, err
			}
			if nodes[index].Kind != 4 {
				candidates = append(candidates, index)
				ends = append(ends, nodes[index].EndLine)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		slices.Sort(ends)
		ends = slices.Compact(ends)
		byStart := func(a, b int) int { return nodes[a].StartLine - nodes[b].StartLine }
		slices.SortStableFunc(candidates, byStart)
		queries := append([]int{}, group...)
		slices.SortStableFunc(queries, byStart)
		width := 1
		for width < len(ends) {
			width *= 2
		}
		tree := make([]parentCandidates, width*2)
		for i := range tree {
			if err := ctx.Err(); err != nil {
				return nil, stats, err
			}
			tree[i] = parentCandidates{-1, -1}
		}
		next := 0
		for _, query := range queries {
			if err := ctx.Err(); err != nil {
				return nil, stats, err
			}
			for next < len(candidates) && nodes[candidates[next]].StartLine <= nodes[query].StartLine {
				candidate := candidates[next]
				position, _ := slices.BinarySearch(ends, nodes[candidate].EndLine)
				position += width
				tree[position] = merge(tree[position], parentCandidates{candidate, -1})
				stats.InsertSteps++
				for position /= 2; position > 0; position /= 2 {
					tree[position] = merge(tree[position*2], tree[position*2+1])
					stats.InsertSteps++
				}
				next++
			}
			first, _ := slices.BinarySearch(ends, nodes[query].EndLine)
			left, right := first+width, width*2
			best := parentCandidates{-1, -1}
			for left < right {
				if left%2 != 0 {
					best = merge(best, tree[left])
					left++
					stats.QuerySegments++
				}
				if right%2 != 0 {
					right--
					best = merge(best, tree[right])
					stats.QuerySegments++
				}
				left, right = left/2, right/2
			}
			stats.Queries++
			parent := best.first
			if parent == query {
				parent = best.second
			}
			if parent >= 0 {
				result[nodes[query].ID] = nodes[parent].ID
			}
		}
	}
	return result, stats, ctx.Err()
}
