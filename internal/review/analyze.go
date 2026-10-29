package review

import (
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
	b := []byte(s)
	quote := byte(0)
	block, lineComment, textBlock, escaped := false, false, false, false
	for i := 0; i < len(b); i++ {
		c := s[i]
		if c == '\n' {
			lineComment = false
			if quote != '\x00' && !textBlock {
				quote = 0
			}
			continue
		}
		if lineComment {
			b[i] = ' '
			continue
		}
		if block {
			b[i] = ' '
			if c == '*' && i+1 < len(b) && s[i+1] == '/' {
				b[i+1] = ' '
				i++
				block = false
			}
			continue
		}
		if textBlock {
			b[i] = ' '
			if c == '"' && i+2 < len(b) && s[i:i+3] == `"""` {
				b[i+1] = ' '
				b[i+2] = ' '
				i += 2
				textBlock = false
			}
			continue
		}
		if quote != 0 {
			b[i] = ' '
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '/' && i+1 < len(b) {
			if s[i+1] == '/' {
				b[i] = ' '
				b[i+1] = ' '
				i++
				lineComment = true
				continue
			}
			if s[i+1] == '*' {
				b[i] = ' '
				b[i+1] = ' '
				i++
				block = true
				continue
			}
		}
		if c == '"' && i+2 < len(b) && s[i:i+3] == `"""` {
			b[i] = ' '
			b[i+1] = ' '
			b[i+2] = ' '
			i += 2
			textBlock = true
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			b[i] = ' '
		}
	}
	return string(b)
}
func javaTokens(s string) []javaToken {
	out := []javaToken{}
	line := 1
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\n' {
			line++
			i++
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' {
			i++
			continue
		}
		j := i + 1
		if c == '_' || c == '$' || c > 127 || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			for j < len(s) {
				c := s[j]
				if c == '_' || c == '$' || c > 127 || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
					j++
				} else {
					break
				}
			}
		}
		out = append(out, javaToken{s[i:j], i, j, line})
		i = j
	}
	return out
}
func matching(tokens []javaToken, start int, open, close string) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		if tokens[i].text == open {
			depth++
		}
		if tokens[i].text == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
func javaIdentifier(v string) bool {
	if v == "" {
		return false
	}
	r := []rune(v)[0]
	return unicode.IsLetter(r) || r == '_' || r == '$'
}
func declarationStart(tokens []javaToken, i int) int {
	for i > 0 {
		p := tokens[i-1].text
		if p == ";" || p == "{" || p == "}" {
			break
		}
		i--
	}
	return i
}
func sourceSlice(s SourceSnapshot, first, last int, complete bool) CodeSlice {
	ls := lines(s.Content)
	first = max(first, 1)
	last = min(last, len(ls))
	return CodeSlice{s.Path, s.Revision, first, last, strings.Join(ls[first-1:last], "\n"), first, last, complete}
}
func readJava(s SourceSnapshot) ([]javaSymbol, []javaUnit, []string) {
	content := strings.ReplaceAll(s.Content, "\r\n", "\n")
	masked := sanitizeJava(content)
	tokens := javaTokens(masked)
	symbols := []javaSymbol{}
	units := []javaUnit{}
	limitations := []string{}
	if strings.Contains(content, `\u`) {
		return symbols, units, []string{s.Path + ": Java Unicode escapes prevent safe declaration analysis."}
	}
	kinds := map[string]int{"class": 0, "interface": 1, "record": 2, "enum": 3}
	for i, t := range tokens {
		kind, ok := kinds[t.text]
		if !ok || i+1 >= len(tokens) || !javaIdentifier(tokens[i+1].text) || i > 0 && tokens[i-1].text == "." {
			continue
		}
		opening := -1
		for j := i + 2; j < len(tokens); j++ {
			if tokens[j].text == ";" {
				break
			}
			if tokens[j].text == "(" {
				end := matching(tokens, j, "(", ")")
				if end < 0 {
					break
				}
				j = end
				continue
			}
			if tokens[j].text == "{" {
				opening = j
				break
			}
		}
		if opening < 0 {
			continue
		}
		end := matching(tokens, opening, "{", "}")
		if end < 0 {
			limitations = append(limitations, s.Path+": incomplete Java type body.")
			continue
		}
		start := declarationStart(tokens, i)
		name := tokens[i+1].text
		signature := strings.Join(strings.Fields(content[tokens[start].start:tokens[opening].start]), " ")
		node := GraphNode{Path: s.Path, Name: name, Signature: signature, Kind: kind, Revision: s.Revision, StartLine: tokens[start].line, EndLine: tokens[end].line}
		symbols = append(symbols, javaSymbol{node: node, key: "type:" + name, content: sourceSlice(s, node.StartLine, node.EndLine, true).Content, startToken: start, openToken: opening, endToken: end})
	}
	// Limit candidates to direct type members so expressions cannot become methods.
	types := append([]javaSymbol{}, symbols...)
	for _, owner := range types {
		depth := 0
		for i := owner.openToken + 1; i < owner.endToken; i++ {
			if tokens[i].text == "{" {
				depth++
				continue
			}
			if tokens[i].text == "}" {
				depth--
				continue
			}
			if depth != 0 || tokens[i].text != "(" || i == 0 || !javaIdentifier(tokens[i-1].text) {
				continue
			}
			name := tokens[i-1].text
			if slices.Contains([]string{"if", "for", "while", "switch", "catch", "synchronized", "new", "try", "this", "super"}, name) {
				continue
			}
			close := matching(tokens, i, "(", ")")
			if close < 0 {
				continue
			}
			next := close + 1
			if next < len(tokens) && tokens[next].text == "throws" {
				for next < len(tokens) && tokens[next].text != "{" && tokens[next].text != ";" {
					next++
				}
			}
			if next >= len(tokens) || (tokens[next].text != "{" && tokens[next].text != ";") {
				i = close
				continue
			}
			start := declarationStart(tokens, i-1)
			prefix := tokens[start : i-1]
			bad := false
			for _, p := range prefix {
				if p.text == "=" || p.text == "new" || p.text == "return" {
					bad = true
				}
			}
			if i >= 2 && tokens[i-2].text == "." {
				bad = true
			}
			if (len(prefix) == 0 && name != owner.node.Name) || bad {
				continue
			}
			end := next
			if tokens[next].text == "{" {
				end = matching(tokens, next, "{", "}")
				if end < 0 {
					limitations = append(limitations, s.Path+": incomplete Java method body.")
					continue
				}
			}
			parameters := 0
			paramTypes := []string{}
			if close > i+1 {
				parameters = 1
				level := 0
				segment := i + 1
				for j := i + 1; j <= close; j++ {
					v := tokens[j].text
					if v == "<" || v == "(" || v == "[" {
						level++
					}
					if v == ">" || v == ")" || v == "]" {
						level--
					}
					if j == close || v == "," && level == 0 {
						part := tokens[segment:j]
						if len(part) > 0 {
							typeParts := []string{}
							for _, p := range part[:max(0, len(part)-1)] {
								if p.text != "final" {
									typeParts = append(typeParts, p.text)
								}
							}
							paramTypes = append(paramTypes, strings.Join(typeParts, ""))
						}
						if j != close {
							parameters++
						}
						segment = j + 1
					}
				}
			}
			signature := strings.Join(strings.Fields(content[tokens[start].start:tokens[next].start]), " ")
			ownerName := owner.node.Name
			node := GraphNode{Path: s.Path, DeclaringType: &ownerName, Name: name, Signature: signature, Kind: 4, Revision: s.Revision, StartLine: tokens[start].line, EndLine: tokens[end].line}
			symbols = append(symbols, javaSymbol{node: node, key: "method:" + ownerName + ":" + name + "(" + strings.Join(paramTypes, ",") + ")", content: sourceSlice(s, node.StartLine, node.EndLine, true).Content, startToken: start, openToken: next, endToken: end, parameters: parameters})
			i = end
		}
	}
	for i := range symbols {
		sym := &symbols[i]
		if sym.node.Kind != 4 {
			parent := ""
			for _, t := range types {
				if t.node.StartLine < sym.node.StartLine && t.node.EndLine >= sym.node.EndLine {
					if parent == "" || len(t.node.Name) > len(parent) {
						parent = t.node.Name
					}
				}
			}
			if parent != "" {
				sym.node.DeclaringType = &parent
				sym.key = parent + ":" + sym.key
			}
		}
		sym.node.SourceSymbolID = Hash(fmt.Sprintf("%d|%s|%s|%d", s.Revision, s.Path, sym.key, sym.node.StartLine))
	}
	for i, sym := range symbols {
		if sym.node.Kind == 4 {
			units = append(units, javaUnit{sym.key, "method", sourceSlice(s, sym.node.StartLine, sym.node.EndLine, true), i})
			continue
		}
		units = append(units, javaUnit{sym.key + ":header", "type-header", sourceSlice(s, sym.node.StartLine, tokens[sym.openToken].line, true), i})
		if tokens[sym.endToken].line > tokens[sym.openToken].line {
			units = append(units, javaUnit{sym.key + ":footer", "type-footer", sourceSlice(s, tokens[sym.endToken].line, tokens[sym.endToken].line, true), i})
		}
		segment := sym.openToken + 1
		for j := segment; j < sym.endToken; j++ {
			childEnd := -1
			for _, child := range symbols {
				if child.startToken == j && child.startToken > sym.openToken && child.endToken < sym.endToken {
					childEnd = child.endToken
					break
				}
			}
			if childEnd >= 0 {
				j = childEnd
				segment = j + 1
				continue
			}
			if tokens[j].text == "{" {
				end := matching(tokens, j, "{", "}")
				if end < 0 {
					break
				}
				j = end
			}
			if tokens[j].text != ";" && tokens[j].text != "}" {
				continue
			}
			if segment <= j {
				kind := "field"
				name := ""
				for k := segment; k <= j; k++ {
					if tokens[k].text == "=" {
						break
					}
					if javaIdentifier(tokens[k].text) {
						name = tokens[k].text
					}
				}
				if tokens[j].text == "}" {
					kind = "declaration"
				}
				key := sym.key + ":" + kind + ":" + name
				units = append(units, javaUnit{key, kind, sourceSlice(s, tokens[segment].line, tokens[j].line, true), i})
			}
			segment = j + 1
		}
	}
	importPattern := regexp.MustCompile(`(?m)^\s*(?:package|import)\s+[^;]+;`)
	imports := importPattern.FindAllStringIndex(masked, -1)
	if len(imports) > 0 {
		first, last := imports[0][0], imports[len(imports)-1][1]
		for first < len(masked) && unicode.IsSpace(rune(masked[first])) {
			first++
		}
		units = append(units, javaUnit{"imports", "imports", sourceSlice(s, strings.Count(masked[:first], "\n")+1, strings.Count(masked[:last], "\n")+1, true), -1})
	}
	slices.SortStableFunc(units, func(a, b javaUnit) int {
		if a.source.StartLine != b.source.StartLine {
			return a.source.StartLine - b.source.StartLine
		}
		return b.source.EndLine - a.source.EndLine
	})
	merged := []javaUnit{}
	for _, u := range units {
		if len(merged) > 0 && u.source.StartLine <= merged[len(merged)-1].source.EndLine {
			last := &merged[len(merged)-1]
			last.kind = "declaration"
			last.source = sourceSlice(s, last.source.StartLine, max(last.source.EndLine, u.source.EndLine), true)
		} else {
			merged = append(merged, u)
		}
	}
	return symbols, merged, limitations
}
func Analyze(snapshot Snapshot) (Document, error) {
	d := Document{Version: 3, ManifestSha256: snapshot.ManifestSha256, BaseSha: snapshot.BaseSha, StartSha: snapshot.StartSha, HeadSha: snapshot.HeadSha, Blocks: []Block{}, Graph: Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Limitations: []string{}}, Coverage: Coverage{IsComplete: snapshot.IsComplete, Exceptions: []string{}, ContextLines: []ChangeDisposition{}, ExcludedLines: []ChangeDisposition{}}, Limitations: append([]string{}, snapshot.Limitations...), DependencyContext: []CodeSlice{}, SelectionVersion: 1, DeclarationContext: []CodeSlice{}}
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
		analysis := fileAnalysis{file: f}
		for _, source := range []*SourceSnapshot{f.Base, f.Head} {
			if source == nil || !strings.HasSuffix(strings.ToLower(source.Path), ".java") {
				continue
			}
			syms, units, limits := readJava(*source)
			offset := len(analysis.symbols)
			for i := range units {
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
			if s.node.Revision == Base {
				baseKeys[s.key] = append(baseKeys[s.key], i)
			} else {
				headKeys[s.key] = append(headKeys[s.key], i)
			}
		}
		for key, before := range baseKeys {
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
			if before.node.Kind != 4 || before.node.Revision != Base || before.node.CounterpartID != nil {
				continue
			}
			candidates := []int{}
			beforeCount := 0
			for j, current := range analysis.symbols {
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
			s := &analysis.symbols[i]
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
			u := &analysis.units[i]
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
		graphIDs[s.node.SourceSymbolID] = fmt.Sprintf("n%d", i+1)
	}
	for _, s := range allSymbols {
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
	for _, n := range d.Graph.Nodes {
		var parent *GraphNode
		for _, p := range d.Graph.Nodes {
			if p.ID != n.ID && p.Kind != 4 && p.Path == n.Path && p.Revision == n.Revision && p.StartLine <= n.StartLine && p.EndLine >= n.EndLine && (parent == nil || p.EndLine-p.StartLine < parent.EndLine-parent.StartLine) {
				copy := p
				parent = &copy
			}
		}
		if parent != nil {
			addEdge(GraphEdge{Kind: "contains", SourceID: parent.ID, TargetIDs: []string{n.ID}, Revision: n.Revision, Change: "structural"})
		}
		if n.Revision == Base && n.CounterpartID != nil {
			addEdge(GraphEdge{Kind: "counterpart", SourceID: n.ID, TargetIDs: []string{*n.CounterpartID}, Revision: Base, Change: "matched-declaration"})
		}
	}
	callsPattern := regexp.MustCompile(`(?:([A-Za-z_$][\w$]*)\s*\.\s*)?([A-Za-z_$][\w$]*)\s*\(`)
	parameters := map[string]int{}
	fileVariables := map[string]map[string]string{}
	for _, s := range allSymbols {
		if s.node.Kind == 4 {
			parameters[graphIDs[s.node.SourceSymbolID]] = s.parameters
		}
	}
	for _, a := range analyses {
		for _, source := range []*SourceSnapshot{a.file.Base, a.file.Head} {
			if source != nil {
				fileVariables[fmt.Sprintf("%d|%s", source.Revision, source.Path)] = variableTypes(source.Content)
			}
		}
	}
	for _, s := range allSymbols {
		if s.node.Kind != 4 {
			continue
		}
		masked := sanitizeJava(s.content)
		for _, m := range callsPattern.FindAllStringSubmatchIndex(masked, -1) {
			name := masked[m[4]:m[5]]
			line := s.node.StartLine + strings.Count(masked[:m[0]], "\n")
			if slices.Contains([]string{"if", "for", "while", "switch", "catch", "synchronized", "try", "this", "super"}, name) || line == s.node.StartLine && name == s.node.Name {
				continue
			}
			receiver := ""
			if m[2] >= 0 {
				receiver = masked[m[2]:m[3]]
			}
			receiverType := receiver
			if receiver == "" || receiver == "this" {
				if s.node.DeclaringType != nil {
					receiverType = *s.node.DeclaringType
				}
			} else {
				if v := fileVariables[fmt.Sprintf("%d|%s", s.node.Revision, s.node.Path)][receiver]; v != "" {
					receiverType = v
				}
				if v := variableTypes(s.content)[receiver]; v != "" {
					receiverType = v
				}
			}
			arity := invocationArity(s.content, m[1]-1)
			targets := []string{}
			for _, target := range d.Graph.Nodes {
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
			addEdge(GraphEdge{Kind: "call", SourceID: graphIDs[s.node.SourceSymbolID], TargetIDs: targets, Revision: s.node.Revision, Line: &line, InvokedName: &nameCopy, ReceiverType: receiverPtr, Resolution: resolution, Change: change})
		}
	}
	byGraphID := map[string]GraphNode{}
	for _, n := range d.Graph.Nodes {
		byGraphID[n.ID] = n
	}
	for i, e := range d.Graph.Edges {
		if e.Kind != "call" {
			continue
		}
		n := byGraphID[e.SourceID]
		if n.CounterpartID == nil {
			continue
		}
		targets := []string{}
		for _, id := range e.TargetIDs {
			target := byGraphID[id]
			if target.CounterpartID == nil {
				targets = append(targets, "?")
			} else {
				targets = append(targets, *target.CounterpartID)
			}
		}
		slices.SortFunc(targets, ordinalCompare)
		for _, other := range d.Graph.Edges {
			otherTargets := append([]string{}, other.TargetIDs...)
			slices.SortFunc(otherTargets, ordinalCompare)
			if other.Kind == "call" && other.SourceID == *n.CounterpartID && canonicalJSON(e.InvokedName) == canonicalJSON(other.InvokedName) && canonicalJSON(e.ReceiverType) == canonicalJSON(other.ReceiverType) && e.Resolution == other.Resolution && slices.Equal(targets, otherTargets) {
				d.Graph.Edges[i].Change = "unchanged"
				break
			}
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
		f := a.file
		for _, sym := range a.symbols {
			if sym.node.Kind == 4 {
				d.DependencyContext = append(d.DependencyContext, CodeSlice{Path: sym.node.Path, Revision: sym.node.Revision, StartLine: sym.node.StartLine, EndLine: sym.node.EndLine, Content: sym.content, SymbolStartLine: sym.node.StartLine, SymbolEndLine: sym.node.EndLine, IsComplete: true})
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
			if _, ok := grouped[u.key]; !ok {
				unitKeys = append(unitKeys, u.key)
			}
			grouped[u.key] = append(grouped[u.key], u)
			if u.kind == "imports" || u.kind == "type-header" || u.kind == "type-footer" {
				shared = append(shared, u.source)
				if u.kind != "type-footer" {
					d.DeclarationContext = append(d.DeclarationContext, u.source)
				}
			}
		}
		owned := map[ChangedLine]bool{}
		contextChanges := []ChangedLine{}
		for _, key := range unitKeys {
			units := grouped[key]
			var before, after *CodeSlice
			ids := []string{}
			ambiguous := false
			for _, u := range units {
				copy := u.source
				if copy.Revision == Base {
					if before != nil {
						ambiguous = true
					}
					before = &copy
				} else {
					if after != nil {
						ambiguous = true
					}
					after = &copy
				}
				if u.symbol >= 0 {
					ids = append(ids, graphIDs[a.symbols[u.symbol].node.SourceSymbolID])
				}
			}
			if ambiguous {
				d.Coverage.Exceptions = append(d.Coverage.Exceptions, f.NewPath+": ambiguous declaration identity "+key)
				continue
			}
			changed := []ChangedLine{}
			for _, ch := range changes {
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
				owned[ch] = true
			}
		}
		if len(d.Blocks) == firstBlock && len(contextChanges) > 0 {
			var before, after *CodeSlice
			for _, u := range a.units {
				if u.kind == "imports" {
					copy := u.source
					if u.source.Revision == Base {
						before = &copy
					} else {
						after = &copy
					}
				}
			}
			importChanges := []ChangedLine{}
			for _, ch := range contextChanges {
				if within(ch, before, after) {
					importChanges = append(importChanges, ch)
				}
			}
			if len(importChanges) > 0 {
				addBlock(f, "imports", before, after, nil, importChanges, []string{"Only imports or package declarations changed; name resolution effects are not established by import text alone."})
				for _, ch := range importChanges {
					owned[ch] = true
				}
			} else {
				addBlock(f, "type-boundary", full(f.Base), full(f.Head), nil, contextChanges, []string{"A changed type boundary requires the complete source for interpretation."})
				for _, ch := range contextChanges {
					owned[ch] = true
				}
			}
		}
		for _, ch := range contextChanges {
			if !owned[ch] {
				d.Coverage.ContextLines = append(d.Coverage.ContextLines, ChangeDisposition{f.NewPath, ch.Revision, ch.Line, "Included as structural context, not a separate review block."})
				owned[ch] = true
			}
		}
		for _, rev := range []Revision{Base, Head} {
			remaining := []int{}
			source := f.Base
			if rev == Head {
				source = f.Head
			}
			for _, ch := range changes {
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
				end := 1
				for end < len(remaining) && remaining[end] == remaining[end-1]+1 {
					end++
				}
				group := remaining[:end]
				v := sourceSlice(*source, group[0], group[len(group)-1], false)
				changed := []ChangedLine{}
				for _, line := range group {
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
			for _, s := range shared {
				if !sliceContains(d.Blocks[i].Base, s) && !sliceContains(d.Blocks[i].Head, s) {
					d.Blocks[i].StructuralContext = append(d.Blocks[i].StructuralContext, s)
				}
			}
		}
	}
	for _, b := range d.Blocks {
		d.Coverage.CoveredChangedLines += len(b.ChangedLines)
		for _, s := range []*CodeSlice{b.Base, b.Head} {
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
