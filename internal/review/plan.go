package review

import (
	"fmt"
	"math"
	"slices"
	"strings"
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
		o.ContextLength = 131072
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
	output := o.OutputTokens
	if output == 0 {
		output = OutputBudget(o.ContextLength, len(b.Blocks), o.Provider)
	}
	if output < 1 || o.SafetyTokens < 0 || o.ContextLength < 1 || output+o.SafetyTokens >= o.ContextLength {
		return PreparedBatch{}, &BudgetError{"REVIEW_BUDGET_INVALID", "The context cannot hold the required output and safety reserve."}
	}
	input := BatchInput{Version: 3, Purpose: ArchitectureReview, BaseSha: d.BaseSha, StartSha: d.StartSha, HeadSha: d.HeadSha, ManifestSha256: d.ManifestSha256, Graph: d.Graph, Batch: b, Limitations: d.Limitations, Task: o.Task}
	user := SerializeInput(input)
	count := CountTokens(o.SystemPrompt, user, ResponseSchema())
	estimated := count.TemplatedInputTokens
	budget := Budget{ContextLength: o.ContextLength, ReservedOutputTokens: output, EstimatedSystemTokens: 0, EstimatedSchemaTokens: count.SchemaReserveTokens, FixedOverheadTokens: o.SafetyTokens, AvailableUserInputTokens: max(0, o.ContextLength-output-count.SchemaReserveTokens-o.SafetyTokens)}
	titles := []string{}
	for _, part := range b.FileParts {
		title := part.Path
		if part.Count > 1 {
			title = fmt.Sprintf("%s (%d/%d)", part.Path, part.Index, part.Count)
		}
		titles = append(titles, title)
	}
	return PreparedBatch{Input: input, Title: strings.Join(titles, ", "), SystemPrompt: o.SystemPrompt, UserPrompt: user, Budget: budget, EstimatedInputTokens: estimated, EstimationMethod: count.Method, InputSha256: Hash(user), MaximumOutputTokens: output, TokenCount: &count, OutputRecoveryEnabled: true}, nil
}
func finalize(groups [][]Block, hash string, complete bool) []Batch {
	files := map[string][]int{}
	for i, group := range groups {
		paths := []string{}
		for _, b := range group {
			paths = append(paths, b.FilePath)
		}
		for _, path := range unique(paths) {
			files[path] = append(files[path], i)
		}
	}
	out := []Batch{}
	for i, group := range groups {
		parts := []FilePart{}
		paths := []string{}
		for _, b := range group {
			paths = append(paths, b.FilePath)
		}
		for _, path := range unique(paths) {
			parts = append(parts, FilePart{path, slices.Index(files[path], i) + 1, len(files[path])})
		}
		out = append(out, Batch{i + 1, len(groups), group, hash, parts, len(groups) == 1 && complete})
	}
	return out
}
func Plan(d Document, o PlanOptions) (PreparedPlan, error) {
	if e := ValidateDocument(d); e != nil {
		return PreparedPlan{}, e
	}
	return plan(d, normalizeOptions(o), 0, false)
}
func plan(d Document, o PlanOptions, inputLimit int, forceSplit bool) (PreparedPlan, error) {
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
func splitBlock(b Block) (Block, Block, bool) {
	length := func(s *CodeSlice) int {
		if s == nil {
			return 0
		}
		return s.EndLine - s.StartLine + 1
	}
	if length(b.Base)+length(b.Head) <= 1 {
		return Block{}, Block{}, false
	}
	divide := func(s *CodeSlice, cut int) (*CodeSlice, *CodeSlice) {
		if s == nil {
			return nil, nil
		}
		ls := lines(s.Content)
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
	for _, ch := range b.ChangedLines {
		if within(ch, a.Base, a.Head) {
			a.ChangedLines = append(a.ChangedLines, ch)
		} else {
			z.ChangedLines = append(z.ChangedLines, ch)
		}
	}
	limitation := "Partial symbol: primary ranges own coverage. Base/head parts are independent source ranges; their part numbers do not prove alignment or preserved behavior."
	a.Limitations = append(append([]string{}, b.Limitations...), limitation)
	z.Limitations = append(append([]string{}, b.Limitations...), limitation)
	return a, z, true
}
func within(ch ChangedLine, before, after *CodeSlice) bool {
	s := before
	if ch.Revision == Head {
		s = after
	}
	return s != nil && ch.Line >= s.StartLine && ch.Line <= s.EndLine
}
func normalizeParts(groups [][]Block) {
	counts := map[string]int{}
	for _, g := range groups {
		for _, b := range g {
			root := b.OriginalBlockID
			if root == "" {
				root = b.ID
			}
			counts[root]++
		}
	}
	indexes := map[string]int{}
	for i, g := range groups {
		for j, b := range g {
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
}
func sourceKeys(blocks []Block) []string {
	out := []string{}
	for _, b := range blocks {
		root := b.OriginalBlockID
		if root == "" {
			root = b.ID
		}
		for _, s := range []*CodeSlice{b.Base, b.Head} {
			if s == nil {
				continue
			}
			for i, line := range lines(s.Content) {
				out = append(out, fmt.Sprintf("%s|%s|%d|%d|%s", root, s.Path, s.Revision, s.StartLine+i, line))
			}
		}
	}
	slices.SortFunc(out, ordinalCompare)
	return out
}
func changeKeys(blocks []Block) []string {
	out := []string{}
	for _, b := range blocks {
		root := b.OriginalBlockID
		if root == "" {
			root = b.ID
		}
		for _, ch := range b.ChangedLines {
			out = append(out, fmt.Sprintf("%s|%d|%d", root, ch.Revision, ch.Line))
		}
	}
	slices.SortFunc(out, ordinalCompare)
	return out
}
func verifyCoverage(original []Block, groups [][]Block) error {
	flat := []Block{}
	for _, g := range groups {
		flat = append(flat, g...)
	}
	if !slices.Equal(sourceKeys(original), sourceKeys(flat)) || !slices.Equal(changeKeys(original), changeKeys(flat)) {
		return fmt.Errorf("source coverage changed while packing review batches")
	}
	return nil
}
func SplitBatch(parent PreparedBatch, o PlanOptions) ([]PreparedBatch, error) {
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
	p, e := plan(d, o, limit, true)
	if e != nil {
		return nil, e
	}
	for i := range p.Batches {
		p.Batches[i].ReplanDepth = parent.ReplanDepth + 1
		p.Batches[i].Input.Batch.IsWholeReview = false
		p.Batches[i], e = RefreshPrepared(p.Batches[i])
		if e != nil {
			return nil, e
		}
	}
	return p.Batches, nil
}
func RefreshPrepared(b PreparedBatch) (PreparedBatch, error) {
	if e := validateBlocksAndGraph(b.Input.Batch.Blocks, b.Input.Graph); e != nil {
		return b, e
	}
	b.UserPrompt = SerializeInput(b.Input)
	b.InputSha256 = Hash(b.UserPrompt)
	count := CountTokens(b.SystemPrompt, b.UserPrompt, ResponseSchema())
	b.TokenCount = &count
	b.EstimatedInputTokens = count.TemplatedInputTokens
	b.EstimationMethod = count.Method
	if b.EstimatedInputTokens > b.Budget.AvailableUserInputTokens {
		return b, &BudgetError{"REVIEW_FINAL_CONTEXT_BUDGET_EXCEEDED", "The refreshed prepared batch exceeds its input budget."}
	}
	return b, nil
}
