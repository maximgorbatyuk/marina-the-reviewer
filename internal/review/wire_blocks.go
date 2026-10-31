package review

import "context"

type wireSlice struct {
	Path       string `json:"path"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	IsComplete bool   `json:"isComplete"`
	Content    string `json:"content"`
}

func makeWireSlice(s *CodeSlice) *wireSlice {
	if s == nil {
		return nil
	}
	return &wireSlice{s.Path, s.StartLine, s.EndLine, s.IsComplete, s.Content}
}

type structural struct {
	Revision string     `json:"revision"`
	Source   *wireSlice `json:"source"`
}
type wireBlock struct {
	ID                string       `json:"id"`
	FilePath          string       `json:"filePath"`
	Kind              string       `json:"kind"`
	SymbolIDs         []string     `json:"symbolIds"`
	BaseExists        bool         `json:"baseExists"`
	HeadExists        bool         `json:"headExists"`
	PartIndex         int          `json:"partIndex"`
	PartCount         int          `json:"partCount"`
	Before            *wireSlice   `json:"before"`
	After             *wireSlice   `json:"after"`
	StructuralContext []structural `json:"structuralContext"`
	ChangedBaseLines  []int        `json:"changedBaseLines"`
	ChangedHeadLines  []int        `json:"changedHeadLines"`
	Limitations       []string     `json:"limitations"`
}

func makeWireBlock(b Block, all map[string]GraphNode, aliases map[string]string) wireBlock {
	value, _ := makeWireBlockContext(context.Background(), b, all, aliases)
	return value
}
func makeWireBlockContext(ctx context.Context, b Block, all map[string]GraphNode, aliases map[string]string) (wireBlock, error) {
	if err := ctx.Err(); err != nil {
		return wireBlock{}, err
	}
	poll := cpuContextPoll{context: ctx}

	w := wireBlock{b.ID, b.FilePath, b.Kind, []string{}, b.BaseExists, b.HeadExists, b.PartIndex, b.PartCount, makeWireSlice(b.Base), makeWireSlice(b.Head), []structural{}, []int{}, []int{}, b.Limitations}
	for _, id := range b.SymbolIDs {
		if err := poll.check(); err != nil {
			return wireBlock{}, err
		}
		if alias, ok := aliases[id]; ok {
			id = alias
		}
		if n, ok := all[id]; ok && (intersects(n, b.Base) || intersects(n, b.Head)) {
			w.SymbolIDs = append(w.SymbolIDs, id)
		}
	}
	for _, s := range b.StructuralContext {
		if err := poll.check(); err != nil {
			return wireBlock{}, err
		}
		rev := "Base"
		if s.Revision == Head {
			rev = "Head"
		}
		w.StructuralContext = append(w.StructuralContext, structural{rev, makeWireSlice(&s)})
	}
	for _, ch := range b.ChangedLines {
		if err := poll.check(); err != nil {
			return wireBlock{}, err
		}
		if ch.Revision == Base {
			w.ChangedBaseLines = append(w.ChangedBaseLines, ch.Line)
		} else {
			w.ChangedHeadLines = append(w.ChangedHeadLines, ch.Line)
		}
	}
	if err := ctx.Err(); err != nil {
		return wireBlock{}, err
	}
	return w, nil
}
