package review

import (
	"context"
	"strings"
)

type contextImportLine struct {
	name        string
	packageLine bool
	malformed   bool
}
type contextImportText struct {
	lines     []string
	items     []contextImportLine
	malformed bool
	full      string
}
type contextTrialState struct {
	blocks      []Block
	contexts    [][]CodeSlice
	primary     contextSpanIndex
	aliases     map[string][]string
	wanted      map[string]bool
	identifiers map[string]bool
	seen        map[contextSliceKey]bool
}

func parseContextImportsContext(ctx context.Context, content string) (contextImportText, error) {
	result := contextImportText{lines: lines(content)}
	result.full = strings.Join(result.lines, "\n")
	for _, line := range result.lines {
		if err := ctx.Err(); err != nil {
			return contextImportText{}, err
		}
		text := strings.TrimSpace(line)
		item := contextImportLine{packageLine: strings.HasPrefix(text, "package ")}
		if match := contextImport.FindStringSubmatch(text); match != nil {
			item.name = match[1]
		} else if strings.HasPrefix(text, "import ") {
			item.malformed = true
			result.malformed = true
		}
		result.items = append(result.items, item)
	}
	return result, ctx.Err()
}

func (index *contextIndex) importText(content string) contextImportText {
	if value, ok := index.importCache[content]; ok {
		return value
	}
	value, err := parseContextImportsContext(index.operationContext(), content)
	if err != nil {
		return contextImportText{}
	}
	cost := 128 + cap(value.lines)*16 + cap(value.items)*32 + len(value.full)
	if index.originalText[content] && cost <= contextImportCacheMaximumBytes-index.importCacheBytes {
		index.importCache[content] = value
		index.importCacheBytes += cost
	}
	return value
}

const contextImportCacheMaximumBytes = 4 * 1024 * 1024

// The result is used by one size probe before the next probe reuses its storage.
func (index *contextIndex) normalizeTrial(batch Batch) Batch {
	if index.trial == nil {
		index.trial = &contextTrialState{primary: contextSpanIndex{}, aliases: map[string][]string{},
			wanted: map[string]bool{}, identifiers: map[string]bool{}, seen: map[contextSliceKey]bool{}}
	}
	state := index.trial
	for key, spans := range state.primary {
		if index.operationErr() != nil {
			return Batch{}
		}
		state.primary[key] = spans[:0]
	}
	for key, paths := range state.aliases {
		if index.operationErr() != nil {
			return Batch{}
		}
		state.aliases[key] = paths[:0]
	}
	clear(state.wanted)
	clear(state.identifiers)
	clear(state.seen)
	for i := range state.contexts {
		if index.operationErr() != nil {
			return Batch{}
		}
		clear(state.contexts[i])
		state.contexts[i] = state.contexts[i][:0]
	}
	state.blocks = append(state.blocks[:0], batch.Blocks...)
	if state.blocks == nil {
		state.blocks = []Block{}
	}
	for len(state.contexts) < len(state.blocks) {
		if index.operationErr() != nil {
			return Batch{}
		}
		state.contexts = append(state.contexts, nil)
	}
	addPrimary := func(source *CodeSlice) {
		if source == nil {
			return
		}
		key := contextSourceKey{source.Path, source.Revision}
		state.primary[key] = append(state.primary[key], contextInterval{source.StartLine, source.EndLine})
	}
	for _, block := range batch.Blocks {
		if index.operationErr() != nil {
			return Batch{}
		}
		addPrimary(block.Base)
		addPrimary(block.Head)
		if block.Base != nil && block.Base.Path != block.FilePath {
			state.aliases[block.Base.Path] = append(state.aliases[block.Base.Path], block.FilePath)
		}
		if index.document.SelectionVersion != 0 {
			for _, source := range block.StructuralContext {
				if !index.imports[contextKey(source)] {
					continue
				}
				for _, item := range index.importText(source.Content).items {
					if item.name != "" && item.name != "*" {
						state.wanted[item.name] = true
					}
				}
			}
		}
	}
	if err := state.primary.sealContext(index.operationContext()); err != nil {
		return Batch{}
	}
	if len(state.wanted) > 0 {
		addWords := func(source CodeSlice) {
			for _, word := range index.wordsFor(source.Content) {
				if index.operationErr() != nil {
					return
				}
				if state.wanted[word] {
					state.identifiers[word] = true
				}
			}
		}
		for _, block := range batch.Blocks {
			if block.Base != nil {
				addWords(*block.Base)
			}
			if block.Head != nil {
				addWords(*block.Head)
			}
			for _, source := range block.StructuralContext {
				if !index.imports[contextKey(source)] {
					addWords(source)
				}
			}
		}
	}
	changed := func(source CodeSlice, line int) bool {
		if index.changed[contextSourceKey{source.Path, source.Revision}][line] {
			return true
		}
		for _, path := range state.aliases[source.Path] {
			if index.changed[contextSourceKey{path, source.Revision}][line] {
				return true
			}
		}
		return false
	}
	for i, block := range batch.Blocks {
		if index.operationErr() != nil {
			return Batch{}
		}
		output := state.contexts[i]
		emit := func(source CodeSlice) {
			key := contextKey(source)
			if !state.primary.covers(source.Path, source.Revision, source.StartLine, source.EndLine) && !state.seen[key] {
				state.seen[key] = true
				output = append(output, source)
			}
		}
		for _, source := range block.StructuralContext {
			if index.document.SelectionVersion == 0 || !index.imports[contextKey(source)] {
				emit(source)
				continue
			}
			imports := index.importText(source.Content)
			malformed := false
			if imports.malformed {
				for line, item := range imports.items {
					if item.malformed && !changed(source, source.StartLine+line) {
						malformed = true
						break
					}
				}
			}
			if malformed {
				emit(source)
				continue
			}
			first, last := -1, -1
			flush := func() {
				if first < 0 {
					return
				}
				part := source
				part.StartLine, part.EndLine = source.StartLine+first, source.StartLine+last
				part.Content, part.IsComplete = imports.full, true
				if first != 0 || last != len(imports.lines)-1 {
					part.Content = strings.Join(imports.lines[first:last+1], "\n")
				}
				emit(part)
			}
			for line, item := range imports.items {
				if !changed(source, source.StartLine+line) && !item.packageLine && item.name != "*" && (item.name == "" || !state.identifiers[item.name]) {
					continue
				}
				if first < 0 {
					first, last = line, line
				} else if line <= last+3 {
					last = line
				} else {
					flush()
					first, last = line, line
				}
			}
			flush()
		}
		state.contexts[i] = output
		if output == nil {
			output = []CodeSlice{}
		}
		block.StructuralContext = output
		state.blocks[i] = block
	}
	batch.Blocks = state.blocks
	return batch
}
