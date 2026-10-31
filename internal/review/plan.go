package review

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

type BudgetError struct{ Code, Message string }

func (e *BudgetError) Error() string { return e.Code + ": " + e.Message }
func OutputBudget(contextLength, blocks int, provider string) int {
	ceiling := min(int(math.Ceil(float64(contextLength)*0.2)), 32768)
	expected := 16384 + blocks*512
	switch strings.ToLower(provider) {
	case "deepseek":
		ceiling = min(int(math.Ceil(float64(contextLength)*0.2)), 65536)
		expected = 65536
	case "mimo":
		ceiling = min(int(math.Ceil(float64(contextLength)*0.2)), 131072)
		expected = 131072
	}
	return min(ceiling, expected)
}
func normalizeOptions(o PlanOptions) PlanOptions {
	if o.SystemPrompt == "" {
		o.SystemPrompt = DefaultSystemPrompt()
	} else if !strings.HasSuffix(o.SystemPrompt, strings.TrimPrefix(SystemPrompt(""), "\n\n")) {
		o.SystemPrompt = SystemPrompt(o.SystemPrompt)
	}
	if o.ContextLength == 0 {
		o.ContextLength = config.DefaultContextLength
	}
	if o.SafetyTokens == 0 {
		o.SafetyTokens = 256 + int(math.Ceil(float64(o.ContextLength)*0.1)) + 384
	}
	if len(o.Task) == 0 {
		o.Task = []byte("null")
	}
	return o
}
func prepareBatch(d Document, b Batch, o PlanOptions) (PreparedBatch, error) {
	return prepareBatchContext(context.Background(), d, b, o)
}
func prepareBatchContext(ctx context.Context, d Document, b Batch, o PlanOptions) (PreparedBatch, error) {
	if err := ctx.Err(); err != nil {
		return PreparedBatch{}, err
	}
	output := o.OutputTokens
	if output == 0 {
		output = OutputBudget(o.ContextLength, len(b.Blocks), o.Provider)
	}
	if output < 1 || o.SafetyTokens < 0 || o.ContextLength < 1 || output+o.SafetyTokens >= o.ContextLength {
		return PreparedBatch{}, &BudgetError{"REVIEW_BUDGET_INVALID", "The context cannot hold the required output and safety reserve."}
	}
	input := BatchInput{Version: 3, Purpose: ArchitectureReview, BaseSha: d.BaseSha, StartSha: d.StartSha, HeadSha: d.HeadSha, ManifestSha256: d.ManifestSha256, Graph: d.Graph, Batch: b, Limitations: d.Limitations, Task: o.Task}
	user, err := SerializeInputContext(ctx, input)
	if err != nil {
		return PreparedBatch{}, err
	}
	count, err := CountTokensContext(ctx, o.SystemPrompt, user, ResponseSchema())
	if err != nil {
		return PreparedBatch{}, err
	}
	estimated := count.TemplatedInputTokens
	budget := Budget{ContextLength: o.ContextLength, ReservedOutputTokens: output, EstimatedSystemTokens: 0, EstimatedSchemaTokens: count.SchemaReserveTokens, FixedOverheadTokens: o.SafetyTokens, AvailableUserInputTokens: max(0, o.ContextLength-output-count.SchemaReserveTokens-o.SafetyTokens)}
	titles := []string{}
	for _, part := range b.FileParts {
		if err := ctx.Err(); err != nil {
			return PreparedBatch{}, err
		}
		title := part.Path
		if part.Count > 1 {
			title = fmt.Sprintf("%s (%d/%d)", part.Path, part.Index, part.Count)
		}
		titles = append(titles, title)
	}
	return PreparedBatch{Input: input, Title: strings.Join(titles, ", "), SystemPrompt: o.SystemPrompt, UserPrompt: user, Budget: budget, EstimatedInputTokens: estimated, EstimationMethod: count.Method, InputSha256: Hash(user), MaximumOutputTokens: output, TokenCount: &count, OutputRecoveryEnabled: true}, nil
}
func finalize(groups [][]Block, hash string, complete bool) []Batch {
	batches, _ := finalizeContext(context.Background(), groups, hash, complete)
	return batches
}
func finalizeContext(ctx context.Context, groups [][]Block, hash string, complete bool) ([]Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	poll := cpuContextPoll{context: ctx}

	files := map[string][]int{}
	for i, group := range groups {
		if err := poll.check(); err != nil {
			return nil, err
		}
		paths := []string{}
		for _, b := range group {
			if err := poll.check(); err != nil {
				return nil, err
			}
			paths = append(paths, b.FilePath)
		}
		for _, path := range unique(paths) {
			if err := poll.check(); err != nil {
				return nil, err
			}
			files[path] = append(files[path], i)
		}
	}
	out := []Batch{}
	for i, group := range groups {
		if err := poll.check(); err != nil {
			return nil, err
		}
		parts := []FilePart{}
		paths := []string{}
		for _, b := range group {
			if err := poll.check(); err != nil {
				return nil, err
			}
			paths = append(paths, b.FilePath)
		}
		for _, path := range unique(paths) {
			if err := poll.check(); err != nil {
				return nil, err
			}
			parts = append(parts, FilePart{path, slices.Index(files[path], i) + 1, len(files[path])})
		}
		out = append(out, Batch{i + 1, len(groups), group, hash, parts, len(groups) == 1 && complete})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
func Plan(d Document, o PlanOptions) (PreparedPlan, error) {
	return PlanContext(context.Background(), d, o)
}
func PlanContext(ctx context.Context, d Document, o PlanOptions) (PreparedPlan, error) {
	if e := ValidateDocumentContext(ctx, d); e != nil {
		return PreparedPlan{}, e
	}
	return planContext(ctx, d, normalizeOptions(o), 0, false)
}
func plan(d Document, o PlanOptions, inputLimit int, forceSplit bool) (PreparedPlan, error) {
	return planContext(context.Background(), d, o, inputLimit, forceSplit)
}
func planContext(ctx context.Context, d Document, o PlanOptions, inputLimit int, forceSplit bool) (PreparedPlan, error) {
	if err := ctx.Err(); err != nil {
		return PreparedPlan{}, err
	}
	p := PreparedPlan{Version: 3, Purpose: ArchitectureReview, GraphSha256: d.Graph.Sha256, Batches: []PreparedBatch{}, Limitations: append([]string{}, d.Limitations...), IsComplete: d.Coverage.IsComplete}
	if len(d.Blocks) == 0 {
		return p, nil
	}
	for _, marker := range []string{"Chunk.Files", "AnchorId", "WHOLE-MR ARCHITECTURE OUTPUT", "Корневые поля — findings и strengths"} {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		if strings.Contains(strings.ToLower(o.SystemPrompt), strings.ToLower(marker)) {
			return p, fmt.Errorf("REVIEW_PROMPT_PROTOCOL_CONFLICT: configured prompt requires the old protocol")
		}
	}
	contexts, err := newContextIndexContext(ctx, d)
	if err != nil {
		return p, err
	}
	sizer, err := newInputSizerContext(ctx, d, o)
	if err != nil {
		return p, err
	}
	var interruption error
	acceptable := func(batch Batch) bool {
		if interruption != nil {
			return false
		}
		if err := ctx.Err(); err != nil {
			interruption = err
			return false
		}
		output := o.OutputTokens
		if output == 0 {
			output = OutputBudget(o.ContextLength, len(batch.Blocks), o.Provider)
		}
		if output < 1 || o.SafetyTokens < 0 || o.ContextLength < 1 || output+o.SafetyTokens >= o.ContextLength {
			return false
		}
		input := BatchInput{Version: 3, Purpose: ArchitectureReview, BaseSha: d.BaseSha, StartSha: d.StartSha, HeadSha: d.HeadSha, ManifestSha256: d.ManifestSha256, Graph: d.Graph, Batch: batch, Limitations: d.Limitations, Task: o.Task}
		count, err := sizer.countContext(ctx, input)
		if err != nil {
			interruption = err
			return false
		}
		available := max(0, o.ContextLength-output-count.SchemaReserveTokens-o.SafetyTokens)
		return count.TemplatedInputTokens <= available && (inputLimit == 0 || count.TemplatedInputTokens <= inputLimit)
	}
	fits := func(blocks []Block) bool {
		batches, err := finalizeContext(ctx, [][]Block{blocks}, d.Graph.Sha256, d.Coverage.IsComplete)
		if err != nil {
			interruption = err
			return false
		}
		batch, err := contexts.normalizeTrialContext(ctx, batches[0])
		if err != nil {
			interruption = err
			return false
		}
		return acceptable(batch)
	}
	groups := [][]Block{}
	wholeFits := false
	if !forceSplit {
		wholeFits = fits(d.Blocks)
	}
	if interruption != nil {
		return p, interruption
	}
	if wholeFits {
		groups = append(groups, d.Blocks)
	} else {
		reason := "The whole review exceeds the context budget; all primary source lines are retained in smaller batches."
		p.SplitReason = &reason
		pending := []Block{}
		semantic, err := semanticGroupsContext(ctx, d)
		if err != nil {
			return p, err
		}
		for _, g := range semantic {
			if err := ctx.Err(); err != nil {
				return p, err
			}
			pending = append(pending, g...)
		}
		pieces := []Block{}
		for len(pending) > 0 {
			if err := ctx.Err(); err != nil {
				return p, err
			}
			b := pending[0]
			pending = pending[1:]
			singleFits := fits([]Block{b})
			if interruption != nil {
				return p, interruption
			}
			if singleFits {
				pieces = append(pieces, b)
				continue
			}
			left, right, ok, splitErr := splitBlockContext(ctx, b)
			if splitErr != nil {
				return p, splitErr
			}
			if !ok {
				return p, &BudgetError{"REVIEW_GRAPH_OR_SOURCE_EXCEEDS_CONTEXT", "The graph, prompt and an indivisible source line exceed the context budget."}
			}
			pending = append([]Block{left, right}, pending...)
		}
		group := []Block{}
		for _, b := range pieces {
			if err := ctx.Err(); err != nil {
				return p, err
			}
			previous := len(group)
			candidate := append(group, b)
			candidateFits := true
			if previous > 0 {
				candidateFits = fits(candidate)
			}
			if interruption != nil {
				return p, interruption
			}
			if previous > 0 && !candidateFits {
				groups = append(groups, group[:previous:previous])
				group = []Block{b}
			} else {
				group = candidate
			}
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
				left, right, ok, splitErr := splitBlockContext(ctx, groups[0][0])
				if splitErr != nil {
					return p, splitErr
				}
				if !ok {
					return p, &BudgetError{"REVIEW_REPLAN_EXHAUSTED", "The failed batch cannot be divided into smaller requests."}
				}
				groups = [][]Block{{left}, {right}}
			}
		}
	}
	if err := normalizePartsContext(ctx, groups); err != nil {
		return p, err
	}
	if e := verifyCoverageContext(ctx, d.Blocks, groups); e != nil {
		return p, e
	}
	finalBatches, err := finalizeContext(ctx, groups, d.Graph.Sha256, d.Coverage.IsComplete && !forceSplit)
	if err != nil {
		return p, err
	}
	for _, batch := range finalBatches {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		batch, err = contexts.normalizeContext(ctx, batch)
		if err != nil {
			return p, err
		}
		desired, err := contexts.decorateDependenciesContext(ctx, batch)
		if err != nil {
			return p, err
		}
		desiredFits := acceptable(desired)
		if interruption != nil {
			return p, interruption
		}
		if desiredFits {
			batch = desired
		} else {
			additions := []dependencyAddition{}
			for i, block := range desired.Blocks {
				if err := ctx.Err(); err != nil {
					return p, err
				}
				if err := ctx.Err(); err != nil {
					return p, err
				}
				existing := map[contextSliceKey]bool{}
				for _, source := range batch.Blocks[i].StructuralContext {
					if err := ctx.Err(); err != nil {
						return p, err
					}
					existing[contextKey(source)] = true
				}
				for _, source := range block.StructuralContext {
					if err := ctx.Err(); err != nil {
						return p, err
					}
					if !existing[contextKey(source)] {
						additions = append(additions, dependencyAddition{owner: i, source: source})
					}
				}
			}
			if err := sortDependencyAdditionsContext(ctx, d.Graph, additions); err != nil {
				return p, err
			}
			omitted := false
			for _, a := range additions {
				if err := ctx.Err(); err != nil {
					return p, err
				}
				if err := ctx.Err(); err != nil {
					return p, err
				}
				candidate := batch
				candidate.Blocks = append([]Block{}, batch.Blocks...)
				candidate.Blocks[a.owner].StructuralContext = append(append([]CodeSlice{}, batch.Blocks[a.owner].StructuralContext...), a.source)
				candidateFits := acceptable(candidate)
				if interruption != nil {
					return p, interruption
				}
				if candidateFits {
					batch = candidate
				} else {
					omitted = true
				}
			}
			if omitted {
				p.Limitations = unique(append(p.Limitations, "Some dependency bodies did not fit this review package. Claims that require those bodies must remain unconfirmed."))
			}
		}
		prepared, e := prepareBatchContext(ctx, d, batch, o)
		if e != nil {
			return p, e
		}
		if prepared.EstimatedInputTokens > prepared.Budget.AvailableUserInputTokens || inputLimit > 0 && prepared.EstimatedInputTokens > inputLimit {
			return p, &BudgetError{"REVIEW_BUDGET_EXCEEDED", "Final batch metadata exceeds the context budget."}
		}
		p.Batches = append(p.Batches, prepared)
	}
	if e := checkContextCoverageContext(ctx, d, p.Batches); e != nil {
		return p, e
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	return p, nil
}
func splitBlock(b Block) (Block, Block, bool) {
	a, z, ok, _ := splitBlockContext(context.Background(), b)
	return a, z, ok
}
func splitBlockContext(ctx context.Context, b Block) (Block, Block, bool, error) {
	if err := ctx.Err(); err != nil {
		return Block{}, Block{}, false, err
	}
	var interruption error

	length := func(s *CodeSlice) int {
		if s == nil {
			return 0
		}
		return s.EndLine - s.StartLine + 1
	}
	if length(b.Base)+length(b.Head) <= 1 {
		return Block{}, Block{}, false, ctx.Err()
	}
	divide := func(s *CodeSlice, cut int) (*CodeSlice, *CodeSlice) {
		if err := ctx.Err(); err != nil {
			interruption = err
			return nil, nil
		}
		if s == nil {
			return nil, nil
		}
		ls := lines(s.Content)
		if err := ctx.Err(); err != nil {
			interruption = err
			return nil, nil
		}
		if cut <= 0 {
			second := *s
			second.IsComplete = false
			return nil, &second
		}
		if cut >= len(ls) {
			first := *s
			first.IsComplete = false
			return &first, nil
		}
		first, second := *s, *s
		first.EndLine = s.StartLine + cut - 1
		first.Content = strings.Join(ls[:cut], "\n")
		first.IsComplete = false
		second.StartLine = s.StartLine + cut
		second.Content = strings.Join(ls[cut:], "\n")
		second.IsComplete = false
		return &first, &second
	}
	a, z := b, b
	a.ID = b.ID + ".p1"
	z.ID = b.ID + ".p2"
	if b.Base != nil && b.Head != nil {
		before, after := *b.Base, *b.Head
		before.IsComplete = false
		after.IsComplete = false
		a.Base = &before
		a.Head = nil
		z.Base = nil
		z.Head = &after
	} else {
		a.Base, z.Base = divide(b.Base, length(b.Base)/2)
		a.Head, z.Head = divide(b.Head, length(b.Head)/2)
	}
	if a.Base == nil && a.Head == nil {
		if b.Base != nil {
			a.Base, z.Base = divide(b.Base, 1)
		} else {
			a.Head, z.Head = divide(b.Head, 1)
		}
	}
	a.ChangedLines = []ChangedLine{}
	z.ChangedLines = []ChangedLine{}
	if interruption != nil {
		return Block{}, Block{}, false, interruption
	}
	for _, ch := range b.ChangedLines {
		if err := ctx.Err(); err != nil {
			return Block{}, Block{}, false, err
		}
		if within(ch, a.Base, a.Head) {
			a.ChangedLines = append(a.ChangedLines, ch)
		} else {
			z.ChangedLines = append(z.ChangedLines, ch)
		}
	}
	limitation := "Partial symbol: primary ranges own coverage. Base/head parts are independent source ranges; their part numbers do not prove alignment or preserved behavior."
	a.Limitations = append(append([]string{}, b.Limitations...), limitation)
	z.Limitations = append(append([]string{}, b.Limitations...), limitation)
	if interruption != nil {
		return Block{}, Block{}, false, interruption
	}
	if err := ctx.Err(); err != nil {
		return Block{}, Block{}, false, err
	}
	return a, z, true, nil
}
func within(ch ChangedLine, before, after *CodeSlice) bool {
	s := before
	if ch.Revision == Head {
		s = after
	}
	return s != nil && ch.Line >= s.StartLine && ch.Line <= s.EndLine
}
func normalizeParts(groups [][]Block) { _ = normalizePartsContext(context.Background(), groups) }
func normalizePartsContext(ctx context.Context, groups [][]Block) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	poll := cpuContextPoll{context: ctx}

	counts := map[string]int{}
	for _, g := range groups {
		if err := poll.check(); err != nil {
			return err
		}
		for _, b := range g {
			if err := poll.check(); err != nil {
				return err
			}
			root := b.OriginalBlockID
			if root == "" {
				root = b.ID
			}
			counts[root]++
		}
	}
	indexes := map[string]int{}
	for i, g := range groups {
		if err := poll.check(); err != nil {
			return err
		}
		for j, b := range g {
			if err := poll.check(); err != nil {
				return err
			}
			root := b.OriginalBlockID
			if root == "" {
				root = b.ID
			}
			indexes[root]++
			b.OriginalBlockID = root
			b.PartIndex = indexes[root]
			b.PartCount = counts[root]
			if counts[root] > 1 {
				b.ID = fmt.Sprintf("%s.p%d", root, b.PartIndex)
			}
			groups[i][j] = b
		}
	}
	return ctx.Err()
}
func sourceKeys(blocks []Block) []string {
	keys, _ := sourceKeysContext(context.Background(), blocks)
	return keys
}
func sourceKeysContext(ctx context.Context, blocks []Block) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	poll := cpuContextPoll{context: ctx}

	out := []string{}
	for _, b := range blocks {
		if err := poll.check(); err != nil {
			return nil, err
		}
		root := b.OriginalBlockID
		if root == "" {
			root = b.ID
		}
		for _, s := range []*CodeSlice{b.Base, b.Head} {
			if err := poll.check(); err != nil {
				return nil, err
			}
			if s == nil {
				continue
			}
			for i, line := range lines(s.Content) {
				if err := poll.check(); err != nil {
					return nil, err
				}
				out = append(out, fmt.Sprintf("%s|%s|%d|%d|%s", root, s.Path, s.Revision, s.StartLine+i, line))
			}
		}
	}
	if err := sortOrdinalContext(ctx, out); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
func changeKeys(blocks []Block) []string {
	keys, _ := changeKeysContext(context.Background(), blocks)
	return keys
}
func changeKeysContext(ctx context.Context, blocks []Block) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	poll := cpuContextPoll{context: ctx}

	out := []string{}
	for _, b := range blocks {
		if err := poll.check(); err != nil {
			return nil, err
		}
		root := b.OriginalBlockID
		if root == "" {
			root = b.ID
		}
		for _, ch := range b.ChangedLines {
			if err := poll.check(); err != nil {
				return nil, err
			}
			out = append(out, fmt.Sprintf("%s|%d|%d", root, ch.Revision, ch.Line))
		}
	}
	if err := sortOrdinalContext(ctx, out); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
func verifyCoverage(original []Block, groups [][]Block) error {
	return verifyCoverageContext(context.Background(), original, groups)
}
func verifyCoverageContext(ctx context.Context, original []Block, groups [][]Block) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	flat := []Block{}
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		flat = append(flat, group...)
	}
	originalSource, err := sourceKeysContext(ctx, original)
	if err != nil {
		return err
	}
	flatSource, err := sourceKeysContext(ctx, flat)
	if err != nil {
		return err
	}
	originalChanges, err := changeKeysContext(ctx, original)
	if err != nil {
		return err
	}
	flatChanges, err := changeKeysContext(ctx, flat)
	if err != nil {
		return err
	}
	if !slices.Equal(originalSource, flatSource) || !slices.Equal(originalChanges, flatChanges) {
		return fmt.Errorf("source coverage changed while packing review batches")
	}
	return ctx.Err()
}
func SplitBatch(parent PreparedBatch, o PlanOptions) ([]PreparedBatch, error) {
	return SplitBatchContext(context.Background(), parent, o)
}
func SplitBatchContext(ctx context.Context, parent PreparedBatch, o PlanOptions) ([]PreparedBatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if parent.ReplanDepth >= 3 {
		return nil, &BudgetError{"REVIEW_REPLAN_EXHAUSTED", "The maximum context-overflow replan depth is three."}
	}
	if o.SystemPrompt == "" {
		o.SystemPrompt = parent.SystemPrompt
	}
	if o.ContextLength == 0 {
		o.ContextLength = parent.Budget.ContextLength
	}
	if o.OutputTokens == 0 {
		o.OutputTokens = parent.MaximumOutputTokens
	}
	if len(o.Task) == 0 {
		o.Task = parent.Input.Task
	}
	o = normalizeOptions(o)
	limit := min(parent.Budget.AvailableUserInputTokens, int(math.Floor(float64(parent.EstimatedInputTokens)*0.75)))
	d := Document{Version: 3, ManifestSha256: parent.Input.ManifestSha256, BaseSha: parent.Input.BaseSha, StartSha: parent.Input.StartSha, HeadSha: parent.Input.HeadSha, Blocks: parent.Input.Batch.Blocks, Graph: parent.Input.Graph, Coverage: Coverage{IsComplete: true}, Limitations: parent.Input.Limitations}
	p, e := planContext(ctx, d, o, limit, true)
	if e != nil {
		return nil, e
	}
	for i := range p.Batches {
		p.Batches[i].ReplanDepth = parent.ReplanDepth + 1
		p.Batches[i].Input.Batch.IsWholeReview = false
		p.Batches[i], e = RefreshPreparedContext(ctx, p.Batches[i])
		if e != nil {
			return nil, e
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.Batches, nil
}
func RefreshPrepared(b PreparedBatch) (PreparedBatch, error) {
	return RefreshPreparedContext(context.Background(), b)
}
func RefreshPreparedContext(ctx context.Context, b PreparedBatch) (PreparedBatch, error) {
	if e := validateBlocksAndGraphContext(ctx, b.Input.Batch.Blocks, b.Input.Graph); e != nil {
		return b, e
	}
	user, err := SerializeInputContext(ctx, b.Input)
	if err != nil {
		return b, err
	}
	b.UserPrompt = user
	b.InputSha256 = Hash(b.UserPrompt)
	count, err := CountTokensContext(ctx, b.SystemPrompt, b.UserPrompt, ResponseSchema())
	if err != nil {
		return b, err
	}
	b.TokenCount = &count
	b.EstimatedInputTokens = count.TemplatedInputTokens
	b.EstimationMethod = count.Method
	if b.EstimatedInputTokens > b.Budget.AvailableUserInputTokens {
		return b, &BudgetError{"REVIEW_FINAL_CONTEXT_BUDGET_EXCEEDED", "The refreshed prepared batch exceeds its input budget."}
	}
	if err := ctx.Err(); err != nil {
		return b, err
	}
	return b, nil
}
