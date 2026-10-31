package review

import (
	"fmt"
	"slices"
	"strings"
)

func planSerializationOracle(d Document, o PlanOptions, inputLimit int, forceSplit bool) (PreparedPlan, error) {
	p := PreparedPlan{Version: 3, Purpose: ArchitectureReview, GraphSha256: d.Graph.Sha256, Batches: []PreparedBatch{}, Limitations: append([]string{}, d.Limitations...), IsComplete: d.Coverage.IsComplete}
	if len(d.Blocks) == 0 {
		return p, nil
	}
	for _, marker := range []string{"Chunk.Files", "AnchorId", "WHOLE-MR ARCHITECTURE OUTPUT", "Корневые поля — findings и strengths"} {
		if strings.Contains(strings.ToLower(o.SystemPrompt), strings.ToLower(marker)) {
			return p, fmt.Errorf("REVIEW_PROMPT_PROTOCOL_CONFLICT: configured prompt requires the old protocol")
		}
	}
	fits := func(blocks []Block) bool {
		batch := finalize([][]Block{blocks}, d.Graph.Sha256, d.Coverage.IsComplete)[0]
		batch = normalizeContext(batch, d)
		prepared, e := prepareBatch(d, batch, o)
		return e == nil && prepared.EstimatedInputTokens <= prepared.Budget.AvailableUserInputTokens && (inputLimit == 0 || prepared.EstimatedInputTokens <= inputLimit)
	}
	groups := [][]Block{}
	if !forceSplit && fits(d.Blocks) {
		groups = append(groups, d.Blocks)
	} else {
		reason := "The whole review exceeds the context budget; all primary source lines are retained in smaller batches."
		p.SplitReason = &reason
		pending := []Block{}
		for _, g := range semanticGroups(d) {
			pending = append(pending, g...)
		}
		pieces := []Block{}
		for len(pending) > 0 {
			b := pending[0]
			pending = pending[1:]
			if fits([]Block{b}) {
				pieces = append(pieces, b)
				continue
			}
			left, right, ok := splitBlock(b)
			if !ok {
				return p, &BudgetError{"REVIEW_GRAPH_OR_SOURCE_EXCEEDS_CONTEXT", "The graph, prompt and an indivisible source line exceed the context budget."}
			}
			pending = append([]Block{left, right}, pending...)
		}
		group := []Block{}
		for _, b := range pieces {
			candidate := append(append([]Block{}, group...), b)
			if len(group) > 0 && !fits(candidate) {
				groups = append(groups, group)
				group = nil
			}
			group = append(group, b)
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
		if forceSplit && len(groups) == 1 {
			if len(groups[0]) > 1 {
				g := groups[0]
				middle := len(g) / 2
				groups = [][]Block{g[:middle], g[middle:]}
			} else {
				left, right, ok := splitBlock(groups[0][0])
				if !ok {
					return p, &BudgetError{"REVIEW_REPLAN_EXHAUSTED", "The failed batch cannot be divided into smaller requests."}
				}
				groups = [][]Block{{left}, {right}}
			}
		}
	}
	normalizeParts(groups)
	if e := verifyCoverage(d.Blocks, groups); e != nil {
		return p, e
	}
	for _, batch := range finalize(groups, d.Graph.Sha256, d.Coverage.IsComplete && !forceSplit) {
		batch = normalizeContext(batch, d)
		desired := decorateDependencies(d, batch)
		acceptable := func(candidate Batch) bool {
			pb, e := prepareBatch(d, candidate, o)
			return e == nil && pb.EstimatedInputTokens <= pb.Budget.AvailableUserInputTokens && (inputLimit == 0 || pb.EstimatedInputTokens <= inputLimit)
		}
		if acceptable(desired) {
			batch = desired
		} else {
			type addition struct {
				owner  int
				source CodeSlice
			}
			additions := []addition{}
			for i, b := range desired.Blocks {
				for _, s := range b.StructuralContext {
					if !slices.ContainsFunc(batch.Blocks[i].StructuralContext, func(existing CodeSlice) bool { return sliceKey(existing) == sliceKey(s) }) {
						additions = append(additions, addition{i, s})
					}
				}
			}
			slices.SortStableFunc(additions, func(a, b addition) int {
				if diff := dependencyPriority(d.Graph, a.source) - dependencyPriority(d.Graph, b.source); diff != 0 {
					return diff
				}
				if len(a.source.Content) != len(b.source.Content) {
					return len(a.source.Content) - len(b.source.Content)
				}
				if diff := ordinalCompare(a.source.Path, b.source.Path); diff != 0 {
					return diff
				}
				if a.source.Revision != b.source.Revision {
					return int(a.source.Revision - b.source.Revision)
				}
				return a.source.StartLine - b.source.StartLine
			})
			omitted := false
			for _, a := range additions {
				candidate := batch
				candidate.Blocks = append([]Block{}, batch.Blocks...)
				candidate.Blocks[a.owner].StructuralContext = append(append([]CodeSlice{}, batch.Blocks[a.owner].StructuralContext...), a.source)
				if acceptable(candidate) {
					batch = candidate
				} else {
					omitted = true
				}
			}
			if omitted {
				p.Limitations = unique(append(p.Limitations, "Some dependency bodies did not fit this review package. Claims that require those bodies must remain unconfirmed."))
			}
		}
		prepared, e := prepareBatch(d, batch, o)
		if e != nil {
			return p, e
		}
		if prepared.EstimatedInputTokens > prepared.Budget.AvailableUserInputTokens || inputLimit > 0 && prepared.EstimatedInputTokens > inputLimit {
			return p, &BudgetError{"REVIEW_BUDGET_EXCEEDED", "Final batch metadata exceeds the context budget."}
		}
		p.Batches = append(p.Batches, prepared)
	}
	if e := checkContextCoverage(d, p.Batches); e != nil {
		return p, e
	}
	return p, nil
}
