package review

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

type javaToken struct {
	text             string
	start, end, line int
}
type javaSymbol struct {
	node                                        GraphNode
	key, content                                string
	startToken, openToken, endToken, parameters int
}
type javaUnit struct {
	key, kind string
	source    CodeSlice
	symbol    int
}

func sanitizeJava(s string) string {
	value, _ := sanitizeJavaContext(context.Background(), s)
	return value
}

func javaIdentifier(v string) bool {
	if v == "" {
		return false
	}
	r := []rune(v)[0]
	return unicode.IsLetter(r) || r == '_' || r == '$'
}

func sourceSlice(s SourceSnapshot, first, last int, complete bool) CodeSlice {
	ls := lines(s.Content)
	first = max(first, 1)
	last = min(last, len(ls))
	return CodeSlice{s.Path, s.Revision, first, last, strings.Join(ls[first-1:last], "\n"), first, last, complete}
}

// Analyze uses a background context for callers without cancellation support.
func Analyze(snapshot Snapshot) (Document, error) {
	return AnalyzeContext(context.Background(), snapshot)
}

// AnalyzeContext builds review blocks and dependency context from pinned source snapshots.
func AnalyzeContext(ctx context.Context, snapshot Snapshot) (Document, error) {
	return analyzeWithSelectionContext(ctx, snapshot, CurrentSelectionVersion)
}
func analyzeWithSelection(snapshot Snapshot, selectionVersion int) (Document, error) {
	return analyzeWithSelectionContext(context.Background(), snapshot, selectionVersion)
}
func analyzeWithSelectionContext(ctx context.Context, snapshot Snapshot, selectionVersion int) (Document, error) {
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	d := Document{Version: 3, ManifestSha256: snapshot.ManifestSha256, BaseSha: snapshot.BaseSha, StartSha: snapshot.StartSha, HeadSha: snapshot.HeadSha, Blocks: []Block{}, Graph: Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Limitations: []string{}}, Coverage: Coverage{IsComplete: snapshot.IsComplete, Exceptions: []string{}, ContextLines: []ChangeDisposition{}, ExcludedLines: []ChangeDisposition{}}, Limitations: append([]string{}, snapshot.Limitations...), DependencyContext: []CodeSlice{}, SelectionVersion: selectionVersion, DeclarationContext: []CodeSlice{}}
	type fileAnalysis struct {
		file    FileSnapshot
		symbols []javaSymbol
		units   []javaUnit
	}
	files := append([]FileSnapshot{}, snapshot.Files...)
	slices.SortStableFunc(files, func(a, b FileSnapshot) int { return ordinalCompare(a.NewPath, b.NewPath) })
	analyses := []fileAnalysis{}
	allSymbols := []javaSymbol{}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		analysis := fileAnalysis{file: f}
		for _, source := range []*SourceSnapshot{f.Base, f.Head} {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			if source == nil {
				continue
			}
			if !strings.HasSuffix(strings.ToLower(source.Path), ".java") {
				if selectionVersion >= 2 {
					symbols, err := readLanguageNeutralFunctionsContext(ctx, *source)
					if err != nil {
						return Document{}, err
					}
					analysis.symbols = append(analysis.symbols, symbols...)
					d.Graph.Limitations = append(d.Graph.Limitations, source.Path+": semantic Java resolution is unavailable; symbol ranges use the language-neutral fallback.")
				}
				continue
			}
			var syms []javaSymbol
			var units []javaUnit
			var limits []string
			if selectionVersion >= 2 {
				var err error
				syms, units, limits, err = readMRJavaContext(ctx, *source)
				if err != nil {
					return Document{}, err
				}
			} else {
				var err error
				syms, units, limits, err = readJavaWithUnicodePolicyContext(ctx, *source, false)
				if err != nil {
					return Document{}, err
				}
			}
			offset := len(analysis.symbols)
			for i := range units {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				if units[i].symbol >= 0 {
					units[i].symbol += offset
				}
			}
			analysis.symbols = append(analysis.symbols, syms...)
			analysis.units = append(analysis.units, units...)
			d.Graph.Limitations = append(d.Graph.Limitations, limits...)
		}
		baseKeys, headKeys := map[string][]int{}, map[string][]int{}
		for i, s := range analysis.symbols {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			if s.node.Revision == Base {
				baseKeys[s.key] = append(baseKeys[s.key], i)
			} else {
				headKeys[s.key] = append(headKeys[s.key], i)
			}
		}
		for key, before := range baseKeys {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			after := headKeys[key]
			if len(before) == 1 && len(after) == 1 {
				a, z := before[0], after[0]
				aID, zID := analysis.symbols[a].node.SourceSymbolID, analysis.symbols[z].node.SourceSymbolID
				analysis.symbols[a].node.CounterpartID = &zID
				analysis.symbols[z].node.CounterpartID = &aID
				change := 0
				if analysis.symbols[a].content != analysis.symbols[z].content {
					change = 2
				}
				analysis.symbols[a].node.Change = change
				analysis.symbols[z].node.Change = change
			}
		}
		for i, before := range analysis.symbols {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			if before.node.Kind != 4 || before.node.Revision != Base || before.node.CounterpartID != nil {
				continue
			}
			candidates := []int{}
			beforeCount := 0
			for j, current := range analysis.symbols {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				sameOwner := canonicalJSON(current.node.DeclaringType) == canonicalJSON(before.node.DeclaringType)
				if current.node.Kind == 4 && current.node.Name == before.node.Name && sameOwner && current.node.CounterpartID == nil {
					if current.node.Revision == Head {
						candidates = append(candidates, j)
					} else {
						beforeCount++
					}
				}
			}
			if beforeCount == 1 && len(candidates) == 1 {
				j := candidates[0]
				left, right := analysis.symbols[i].node.SourceSymbolID, analysis.symbols[j].node.SourceSymbolID
				analysis.symbols[i].node.CounterpartID = &right
				analysis.symbols[j].node.CounterpartID = &left
				analysis.symbols[i].node.Change = 2
				analysis.symbols[j].node.Change = 2
			}
		}
		for i := range analysis.symbols {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			s := &analysis.symbols[i]
			if selectionVersion >= 2 && f.Operation == "context" {
				s.node.Change = 0
				continue
			}
			if s.node.CounterpartID == nil {
				s.node.CounterpartAbsent = true
				if s.node.Revision == Base {
					s.node.Change = 3
				} else {
					s.node.Change = 1
				}
			}
		}
		for i := range analysis.units {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			u := &analysis.units[i]
			if selectionVersion >= 2 && u.symbol >= 0 {
				symbol := analysis.symbols[u.symbol]
				suffix := strings.TrimPrefix(u.key, symbol.node.SourceSymbolID)
				u.key = symbol.node.SourceSymbolID
				if symbol.node.CounterpartID != nil && ordinalCompare(*symbol.node.CounterpartID, u.key) < 0 {
					u.key = *symbol.node.CounterpartID
				}
				u.key += suffix
				continue
			}
			if u.kind == "method" && u.symbol >= 0 {
				sym := analysis.symbols[u.symbol]
				if sym.node.CounterpartID != nil {
					u.key = "method-pair:" + sym.node.SourceSymbolID
					if ordinalCompare(*sym.node.CounterpartID, sym.node.SourceSymbolID) < 0 {
						u.key = "method-pair:" + *sym.node.CounterpartID
					}
				}
			}
		}
		analyses = append(analyses, analysis)
		allSymbols = append(allSymbols, analysis.symbols...)
	}
	slices.SortStableFunc(allSymbols, func(a, b javaSymbol) int {
		if n := ordinalCompare(a.node.Path, b.node.Path); n != 0 {
			return n
		}
		if a.node.Revision != b.node.Revision {
			return int(a.node.Revision - b.node.Revision)
		}
		if a.node.StartLine != b.node.StartLine {
			return a.node.StartLine - b.node.StartLine
		}
		if a.node.EndLine != b.node.EndLine {
			return b.node.EndLine - a.node.EndLine
		}
		return ordinalCompare(a.node.SourceSymbolID, b.node.SourceSymbolID)
	})
	graphIDs := map[string]string{}
	for i, s := range allSymbols {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		graphIDs[s.node.SourceSymbolID] = fmt.Sprintf("n%d", i+1)
		if selectionVersion >= 2 {
			graphIDs[s.node.SourceSymbolID] = s.node.SourceSymbolID
		}
	}
	for _, s := range allSymbols {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		n := s.node
		n.ID = graphIDs[n.SourceSymbolID]
		if n.CounterpartID != nil {
			id := graphIDs[*n.CounterpartID]
			n.CounterpartID = &id
		}
		d.Graph.Nodes = append(d.Graph.Nodes, n)
	}
	addEdge := func(e GraphEdge) {
		e.ID = fmt.Sprintf("e%d", len(d.Graph.Edges)+1)
		if e.TargetIDs == nil {
			e.TargetIDs = []string{}
		}
		d.Graph.Edges = append(d.Graph.Edges, e)
	}
	parents, _, err := graphParentsContext(ctx, d.Graph.Nodes)
	if err != nil {
		return Document{}, err
	}
	for _, n := range d.Graph.Nodes {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		if parent, found := parents[n.ID]; found {
			addEdge(GraphEdge{Kind: "contains", SourceID: parent, TargetIDs: []string{n.ID}, Revision: n.Revision, Change: "structural"})
		}
		if n.Revision == Base && n.CounterpartID != nil {
			addEdge(GraphEdge{Kind: "counterpart", SourceID: n.ID, TargetIDs: []string{*n.CounterpartID}, Revision: Base, Change: "matched-declaration"})
		}
	}
	callsPattern := regexp.MustCompile(`(?:([A-Za-z_$][\w$]*)\s*\.\s*)?([A-Za-z_$][\w$]*)\s*\(`)
	parameters := map[string]int{}
	fileVariables := map[string]map[string]string{}
	for _, s := range allSymbols {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		if s.node.Kind == 4 {
			parameters[graphIDs[s.node.SourceSymbolID]] = s.parameters
		}
	}
	for _, a := range analyses {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		if selectionVersion >= 2 {
			break
		}
		for _, source := range []*SourceSnapshot{a.file.Base, a.file.Head} {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			if source != nil {
				types := variableTypes(source.Content)
				fileVariables[fmt.Sprintf("%d|%s", source.Revision, source.Path)] = types
				conflicts := []string{}
				for name, typ := range types {
					if err := ctx.Err(); err != nil {
						return Document{}, err
					}
					if typ == "" {
						conflicts = append(conflicts, name)
					}
				}
				slices.SortFunc(conflicts, ordinalCompare)
				for _, name := range conflicts {
					if err := ctx.Err(); err != nil {
						return Document{}, err
					}
					d.Graph.Limitations = append(d.Graph.Limitations, fmt.Sprintf("%s: receiver variable %q has conflicting declaration types across scopes; calls through that name remain unresolved.", source.Path, name))
				}
			}
		}
	}
	var resolver *mrCallResolver
	if selectionVersion >= 2 {
		var err error
		resolver, err = newMRCallResolverContext(ctx, allSymbols, files, graphIDs)
		if err != nil {
			return Document{}, err
		}
	}
	seenCalls := map[string]bool{}
	for _, s := range allSymbols {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		if s.node.Kind != 4 {
			continue
		}
		bodyOffset := 0
		if selectionVersion >= 2 {
			opening := javaBodyOpening(s.content)
			if opening < 0 {
				continue
			}
			bodyOffset = opening + 1
		}
		masked, err := sanitizeJavaContext(ctx, s.content[bodyOffset:])
		if err != nil {
			return Document{}, err
		}
		methodVariables := variableTypes(s.content)
		line, previousOffset := s.node.StartLine+strings.Count(s.content[:bodyOffset], "\n"), 0
		for _, m := range callsPattern.FindAllStringSubmatchIndex(masked, -1) {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			name := masked[m[4]:m[5]]
			line += strings.Count(masked[previousOffset:m[0]], "\n")
			previousOffset = m[0]
			if slices.Contains([]string{"if", "for", "while", "switch", "catch", "synchronized", "try", "this", "super"}, name) || line == s.node.StartLine && name == s.node.Name {
				continue
			}
			receiver := ""
			if m[2] >= 0 {
				receiver = masked[m[2]:m[3]]
			}
			receiverType := receiver
			unknownReceiver := false
			if receiver == "" || receiver == "this" {
				if s.node.DeclaringType != nil {
					receiverType = *s.node.DeclaringType
				}
			} else {
				fileType, fileKnown := fileVariables[fmt.Sprintf("%d|%s", s.node.Revision, s.node.Path)][receiver]
				methodType, methodKnown := methodVariables[receiver]
				if fileKnown && fileType == "" || methodKnown && methodType == "" {
					unknownReceiver = true
					receiverType = ""
				} else if methodKnown {
					receiverType = methodType
				} else if fileKnown {
					receiverType = fileType
				}
			}
			arity := invocationArity(s.content, bodyOffset+m[1]-1)
			targets := []string{}
			for _, target := range d.Graph.Nodes {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				if selectionVersion >= 2 {
					break
				}
				if unknownReceiver {
					continue
				}
				if target.Kind != 4 || target.Revision != s.node.Revision || target.Name != name {
					continue
				}
				sameOwner := target.DeclaringType != nil && s.node.DeclaringType != nil && *target.DeclaringType == *s.node.DeclaringType
				if receiver == "" || receiver == "this" {
					if !sameOwner {
						continue
					}
				} else if target.DeclaringType == nil || *target.DeclaringType != receiverType {
					continue
				}
				if arity >= 0 && parameters[target.ID] != arity {
					continue
				}
				targets = append(targets, target.ID)
			}
			resolution := 0
			if len(targets) == 0 {
				resolution = 2
			} else if len(targets) > 1 {
				resolution = 1
			}
			change := "added"
			if s.node.Revision == Base {
				change = "removed"
			}
			if s.node.Change == 0 {
				change = "unchanged"
			}
			nameCopy := name
			var receiverPtr *string
			if receiverType != "" {
				r := receiverType
				receiverPtr = &r
			}
			if resolver != nil {
				targets, receiverPtr, resolution = resolver.resolve(s, name, receiver, arity)
				key := canonicalJSON([]any{s.node.SourceSymbolID, name, receiver, line, targets})
				if seenCalls[key] {
					continue
				}
				seenCalls[key] = true
			}
			callLine := line
			addEdge(GraphEdge{Kind: "call", SourceID: graphIDs[s.node.SourceSymbolID], TargetIDs: targets, Revision: s.node.Revision, Line: &callLine, InvokedName: &nameCopy, ReceiverType: receiverPtr, Resolution: resolution, Change: change})
		}
	}
	if _, err := markUnchangedCallsContext(ctx, &d.Graph); err != nil {
		return Document{}, err
	}
	relevantBodies := map[string]bool{}
	if selectionVersion >= 2 {
		var err error
		d.Graph, graphIDs, relevantBodies, err = selectMRGraphContext(ctx, d.Graph, allSymbols)
		if err != nil {
			return Document{}, err
		}
	}
	d.Graph.Limitations = unique(append(d.Graph.Limitations, "Java call resolution is lexical and limited to visible declarations. Reflection, inheritance, overload types and runtime dispatch are not proven."))
	d.Graph.Sha256 = Hash(canonicalJSON(struct {
		Nodes       []GraphNode
		Edges       []GraphEdge
		Limitations []string
	}{d.Graph.Nodes, d.Graph.Edges, d.Graph.Limitations}))
	addBlock := func(f FileSnapshot, kind string, before, after *CodeSlice, ids []string, changed []ChangedLine, limits []string) {
		id := fmt.Sprintf("b%d", len(d.Blocks)+1)
		if ids == nil {
			ids = []string{}
		}
		if limits == nil {
			limits = []string{}
		}
		baseExists, headExists := before != nil, after != nil
		if kind == "diff-region" || kind == "unparsed-change" {
			baseExists, headExists = f.Base != nil, f.Head != nil
		}
		d.Blocks = append(d.Blocks, Block{ID: id, OriginalBlockID: id, FilePath: f.NewPath, Kind: kind, SymbolIDs: ids, Base: before, Head: after, BaseExists: baseExists, HeadExists: headExists, PartIndex: 1, PartCount: 1, ChangedLines: changed, Limitations: limits, StructuralContext: []CodeSlice{}})
	}
	for _, a := range analyses {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		f := a.file
		for _, sym := range a.symbols {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			if sym.node.Kind == 4 && (selectionVersion < 2 || relevantBodies[sym.node.SourceSymbolID]) {
				d.DependencyContext = append(d.DependencyContext, CodeSlice{Path: sym.node.Path, Revision: sym.node.Revision, StartLine: sym.node.StartLine, EndLine: sym.node.EndLine, Content: sym.content, SymbolStartLine: sym.node.StartLine, SymbolEndLine: sym.node.EndLine, IsComplete: true})
			}
		}
		if selectionVersion >= 2 {
			for _, unit := range a.units {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				if unit.kind == "imports" || unit.kind == "type-header" {
					d.DeclarationContext = append(d.DeclarationContext, unit.source)
				}
			}
		}
		if f.Operation == "context" {
			continue
		}
		changes := distinct(f.ChangedLines)
		d.Coverage.ExpectedChangedLines += len(changes)
		if f.SkipReason != "" || f.IsBinary || f.IsSubmodule || f.IsLFS {
			reason := f.NewPath + ": source unavailable (" + f.SkipReason + ")."
			d.Coverage.Exceptions = append(d.Coverage.Exceptions, reason)
			continue
		}
		if len(changes) == 0 {
			continue
		}
		java := strings.HasSuffix(strings.ToLower(f.NewPath), ".java")
		if java && f.Base != nil && f.Head != nil && strings.TrimRight(strings.ReplaceAll(f.Base.Content, "\r\n", "\n"), "\n") == strings.TrimRight(strings.ReplaceAll(f.Head.Content, "\r\n", "\n"), "\n") {
			for _, ch := range changes {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				d.Coverage.ExcludedLines = append(d.Coverage.ExcludedLines, ChangeDisposition{f.NewPath, ch.Revision, ch.Line, "Only line endings or the final newline changed in Java source."})
			}
			continue
		}
		full := func(s *SourceSnapshot) *CodeSlice {
			if s == nil {
				return nil
			}
			v := sourceSlice(*s, 1, len(lines(s.Content)), true)
			return &v
		}
		if !java || len(a.units) == 0 {
			if !java && max(sourceLineCount(f.Base), sourceLineCount(f.Head)) > 200 && len(f.Hunks) > 0 {
				for _, h := range f.Hunks {
					if err := ctx.Err(); err != nil {
						return Document{}, err
					}
					region := func(s *SourceSnapshot, start, count int) *CodeSlice {
						if s == nil || start < 1 || count == 0 {
							return nil
						}
						v := sourceSlice(*s, start, start+count-1, false)
						return &v
					}
					before, after := region(f.Base, h.OldStart, h.OldCount), region(f.Head, h.NewStart, h.NewCount)
					owned := []ChangedLine{}
					for _, ch := range changes {
						if err := ctx.Err(); err != nil {
							return Document{}, err
						}
						if within(ch, before, after) {
							owned = append(owned, ch)
						}
					}
					if len(owned) > 0 {
						addBlock(f, "diff-region", before, after, nil, owned, []string{"This diff region is not the complete file or a proven syntax boundary."})
					}
				}
			} else {
				addBlock(f, "file", full(f.Base), full(f.Head), nil, changes, []string{"The complete file is included because declaration boundaries are unavailable for this source."})
			}
			continue
		}
		firstBlock := len(d.Blocks)
		unitKeys := []string{}
		grouped := map[string][]javaUnit{}
		shared := []CodeSlice{}
		for _, u := range a.units {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			if _, ok := grouped[u.key]; !ok {
				unitKeys = append(unitKeys, u.key)
			}
			grouped[u.key] = append(grouped[u.key], u)
			if u.kind == "imports" || u.kind == "type-header" || u.kind == "type-footer" {
				shared = append(shared, u.source)
				if u.kind != "type-footer" && selectionVersion < 2 {
					d.DeclarationContext = append(d.DeclarationContext, u.source)
				}
			}
		}
		owned := map[ChangedLine]bool{}
		contextChanges := []ChangedLine{}
		for _, key := range unitKeys {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			units := grouped[key]
			var before, after *CodeSlice
			ids := []string{}
			ambiguous := false
			for _, u := range units {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				cloned := u.source
				if cloned.Revision == Base {
					if before != nil {
						ambiguous = true
					}
					before = &cloned
				} else {
					if after != nil {
						ambiguous = true
					}
					after = &cloned
				}
				if u.symbol >= 0 {
					if id, found := graphIDs[a.symbols[u.symbol].node.SourceSymbolID]; found {
						ids = append(ids, id)
					}
				}
			}
			if ambiguous {
				d.Coverage.Exceptions = append(d.Coverage.Exceptions, f.NewPath+": ambiguous declaration identity "+key)
				continue
			}
			changed := []ChangedLine{}
			for _, ch := range changes {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				if within(ch, before, after) {
					changed = append(changed, ch)
				}
			}
			if len(changed) == 0 {
				continue
			}
			kind := units[0].kind
			if kind == "imports" || kind == "type-footer" {
				contextChanges = append(contextChanges, changed...)
				continue
			}
			addBlock(f, kind, before, after, unique(ids), changed, nil)
			for _, ch := range changed {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				owned[ch] = true
			}
		}
		if len(d.Blocks) == firstBlock && len(contextChanges) > 0 {
			var before, after *CodeSlice
			for _, u := range a.units {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				if u.kind == "imports" {
					cloned := u.source
					if u.source.Revision == Base {
						before = &cloned
					} else {
						after = &cloned
					}
				}
			}
			importChanges := []ChangedLine{}
			for _, ch := range contextChanges {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				if within(ch, before, after) {
					importChanges = append(importChanges, ch)
				}
			}
			if len(importChanges) > 0 {
				addBlock(f, "imports", before, after, nil, importChanges, []string{"Only imports or package declarations changed; name resolution effects are not established by import text alone."})
				for _, ch := range importChanges {
					if err := ctx.Err(); err != nil {
						return Document{}, err
					}
					owned[ch] = true
				}
			} else {
				addBlock(f, "type-boundary", full(f.Base), full(f.Head), nil, contextChanges, []string{"A changed type boundary requires the complete source for interpretation."})
				for _, ch := range contextChanges {
					if err := ctx.Err(); err != nil {
						return Document{}, err
					}
					owned[ch] = true
				}
			}
		}
		for _, ch := range contextChanges {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			if !owned[ch] {
				d.Coverage.ContextLines = append(d.Coverage.ContextLines, ChangeDisposition{f.NewPath, ch.Revision, ch.Line, "Included as structural context, not a separate review block."})
				owned[ch] = true
			}
		}
		for _, rev := range []Revision{Base, Head} {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			remaining := []int{}
			source := f.Base
			if rev == Head {
				source = f.Head
			}
			for _, ch := range changes {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				if ch.Revision != rev || owned[ch] {
					continue
				}
				if source == nil || ch.Line < 1 || ch.Line > len(lines(source.Content)) {
					d.Coverage.Exceptions = append(d.Coverage.Exceptions, fmt.Sprintf("%s:%d:%d: source line unavailable.", f.NewPath, rev, ch.Line))
					continue
				}
				if strings.TrimSpace(lines(source.Content)[ch.Line-1]) == "" {
					d.Coverage.ExcludedLines = append(d.Coverage.ExcludedLines, ChangeDisposition{f.NewPath, rev, ch.Line, "Blank line outside a declaration."})
					continue
				}
				remaining = append(remaining, ch.Line)
			}
			slices.Sort(remaining)
			for len(remaining) > 0 {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				end := 1
				for end < len(remaining) && remaining[end] == remaining[end-1]+1 {
					if err := ctx.Err(); err != nil {
						return Document{}, err
					}
					end++
				}
				group := remaining[:end]
				v := sourceSlice(*source, group[0], group[len(group)-1], false)
				changed := []ChangedLine{}
				for _, line := range group {
					if err := ctx.Err(); err != nil {
						return Document{}, err
					}
					changed = append(changed, ChangedLine{rev, line})
				}
				var before, after *CodeSlice
				if rev == Base {
					before = &v
				} else {
					after = &v
				}
				addBlock(f, "unparsed-change", before, after, nil, changed, []string{"Declaration boundaries are uncertain; this changed region has no proven counterpart."})
				remaining = remaining[end:]
			}
		}
		slices.SortStableFunc(shared, func(a, b CodeSlice) int {
			if a.StartLine != b.StartLine {
				return a.StartLine - b.StartLine
			}
			return int(a.Revision - b.Revision)
		})
		for i := firstBlock; i < len(d.Blocks); i++ {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			for _, s := range shared {
				if err := ctx.Err(); err != nil {
					return Document{}, err
				}
				if !sliceContains(d.Blocks[i].Base, s) && !sliceContains(d.Blocks[i].Head, s) {
					d.Blocks[i].StructuralContext = append(d.Blocks[i].StructuralContext, s)
				}
			}
		}
	}
	for _, b := range d.Blocks {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		d.Coverage.CoveredChangedLines += len(b.ChangedLines)
		for _, s := range []*CodeSlice{b.Base, b.Head} {
			if err := ctx.Err(); err != nil {
				return Document{}, err
			}
			if s != nil {
				d.Coverage.RequiredSourceLines += s.EndLine - s.StartLine + 1
			}
		}
	}
	if d.Coverage.CoveredChangedLines+len(d.Coverage.ContextLines)+len(d.Coverage.ExcludedLines) != d.Coverage.ExpectedChangedLines {
		d.Coverage.Exceptions = append(d.Coverage.Exceptions, "Changed-line coverage is incomplete.")
	}
	d.Coverage.IsComplete = d.Coverage.IsComplete && len(d.Coverage.Exceptions) == 0
	d.Limitations = unique(append(append(d.Limitations, d.Graph.Limitations...), d.Coverage.Exceptions...))
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	return d, nil
}
func sourceLineCount(s *SourceSnapshot) int {
	if s == nil {
		return 0
	}
	return len(lines(s.Content))
}
func sliceContains(s *CodeSlice, inner CodeSlice) bool {
	return s != nil && s.Path == inner.Path && s.Revision == inner.Revision && s.StartLine <= inner.StartLine && s.EndLine >= inner.EndLine
}
