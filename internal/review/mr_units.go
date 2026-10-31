package review

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var mrFieldNamePattern = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*(?:\[\s*\])*\s*$`)
var mrImportsPattern = regexp.MustCompile(`(?m)^\s*(?:package|import)\s+[^;]+;`)
var mrNestedTypePattern = regexp.MustCompile(`\b(?:class|interface|record|enum)\s+([A-Za-z_$][\w$]*)`)

func readMRJavaContext(ctx context.Context, source SourceSnapshot) ([]javaSymbol, []javaUnit, []string, error) {
	symbols, _, limitations, err := readJavaWithUnicodePolicyContext(ctx, source, true)
	if err != nil {
		return nil, nil, nil, err
	}
	type enumRegion struct{ opening, end int }
	enumConstants := map[string][]enumRegion{}
	var tokens []javaToken
	for _, symbol := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		if symbol.node.Kind != 3 {
			continue
		}
		if tokens == nil {
			masked, err := sanitizeJavaContext(ctx, source.Content)
			if err != nil {
				return nil, nil, nil, err
			}
			rawTokens, err := javaTokensContext(ctx, masked)
			if err != nil {
				return nil, nil, nil, err
			}
			tokens, err = declarationTokensContext(ctx, rawTokens)
			if err != nil {
				return nil, nil, nil, err
			}
		}
		braces, parentheses, end := 0, 0, symbol.endToken
		for i := symbol.openToken + 1; i < symbol.endToken; i++ {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, err
			}
			switch tokens[i].text {
			case "(":
				parentheses++
			case ")":
				parentheses--
			case "{":
				braces++
			case "}":
				braces--
			case ";":
				if braces == 0 && parentheses == 0 {
					end = i
				}
			}
			if end == i {
				break
			}
		}
		enumConstants[symbol.node.Name] = append(enumConstants[symbol.node.Name], enumRegion{symbol.openToken, end})
	}
	declarationsOnly := make([]javaSymbol, 0, len(symbols))
	for _, symbol := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		if symbol.node.Kind == 4 {
			match := mrNestedTypePattern.FindStringSubmatch(symbol.node.Signature)
			if len(match) > 1 && match[1] == symbol.node.Name {
				continue
			}
			constant := false
			if symbol.node.DeclaringType != nil {
				for _, region := range enumConstants[*symbol.node.DeclaringType] {
					if err := ctx.Err(); err != nil {
						return nil, nil, nil, err
					}
					if symbol.startToken > region.opening && symbol.startToken < region.end && symbol.endToken <= region.end {
						constant = true
						break
					}
				}
			}
			if constant {
				continue
			}
		}
		declarationsOnly = append(declarationsOnly, symbol)
	}
	symbols = declarationsOnly
	content := strings.ReplaceAll(source.Content, "\r\n", "\n")
	source.Content = content
	masked, err := sanitizeJavaContext(ctx, content)
	if err != nil {
		return nil, nil, nil, err
	}
	sourceLines, maskedLines := lines(content), lines(masked)
	bodyRanges := [][2]int{}
	declarations := map[int][]int{}
	for i, symbol := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		if symbol.node.Kind != 4 {
			continue
		}
		if javaBodyOpening(symbol.content) >= 0 {
			bodyRanges = append(bodyRanges, [2]int{symbol.node.StartLine, symbol.node.EndLine})
		} else {
			declarations[symbol.node.EndLine] = append(declarations[symbol.node.EndLine], i)
		}
	}
	slices.SortFunc(bodyRanges, func(a, b [2]int) int { return a[0] - b[0] })
	merged := [][2]int{}
	for _, interval := range bodyRanges {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		if len(merged) > 0 && interval[0] <= merged[len(merged)-1][1] {
			merged[len(merged)-1][1] = max(merged[len(merged)-1][1], interval[1])
		} else {
			merged = append(merged, interval)
		}
	}
	coveredByBody := func(line int) bool {
		index, _ := slices.BinarySearchFunc(merged, line, func(interval [2]int, line int) int { return interval[1] - line })
		return index < len(merged) && merged[index][0] <= line
	}
	for _, owner := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		if owner.node.Kind != 0 && owner.node.Kind != 1 {
			continue
		}
		opening := javaBodyOpening(owner.content)
		if opening < 0 {
			continue
		}
		start := owner.node.StartLine + strings.Count(owner.content[:opening+1], "\n") + 1
		for line := start; line < owner.node.EndLine; line++ {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, err
			}
			if coveredByBody(line) {
				start = line + 1
				continue
			}
			if !strings.HasSuffix(strings.TrimSpace(maskedLines[line-1]), ";") {
				continue
			}
			first := start
			start = line + 1
			for first < line && strings.TrimSpace(sourceLines[first-1]) == "" {
				if err := ctx.Err(); err != nil {
					return nil, nil, nil, err
				}
				first++
			}
			for _, i := range declarations[line] {
				if err := ctx.Err(); err != nil {
					return nil, nil, nil, err
				}
				method := &symbols[i]
				if method.node.StartLine >= first && method.node.DeclaringType != nil && *method.node.DeclaringType == owner.node.Name {
					method.node.StartLine = first
					method.content = sourceSlice(source, first, line, true).Content
					method.node.SourceSymbolID = Hash(fmt.Sprintf("%d|%s|%s|%d", source.Revision, source.Path, method.key, first))
				}
			}
		}
	}
	for i := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		normalizeMRSymbol(&symbols[i])
	}
	units, err := mrJavaUnitsContext(ctx, source, symbols)
	if err != nil {
		return nil, nil, nil, err
	}
	return symbols, units, limitations, ctx.Err()
}

func mrJavaUnitsContext(ctx context.Context, source SourceSnapshot, symbols []javaSymbol) ([]javaUnit, error) {
	content := strings.ReplaceAll(source.Content, "\r\n", "\n")
	masked, err := sanitizeJavaContext(ctx, content)
	if err != nil {
		return nil, err
	}
	sourceLines := lines(content)
	starts := make([]int, len(sourceLines))
	for i := 1; i < len(starts); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		starts[i] = starts[i-1] + len(sourceLines[i-1]) + 1
	}
	lineAt := func(position int) int {
		index, found := slices.BinarySearch(starts, position)
		if found {
			return index + 1
		}
		return index
	}
	result := []javaUnit{}
	add := func(key, kind string, first, last, symbol int) {
		content := strings.Join(sourceLines[first-1:last], "\n")
		slice := CodeSlice{Path: source.Path, Revision: source.Revision, StartLine: first, EndLine: last, Content: content, SymbolStartLine: first, SymbolEndLine: last, IsComplete: true}
		result = append(result, javaUnit{key, kind, slice, symbol})
	}
	for i, method := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if method.node.Kind == 4 {
			add(method.node.SourceSymbolID, "method", method.node.StartLine, method.node.EndLine, i)
		}
	}
	for i, owner := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if owner.node.Kind == 4 {
			continue
		}
		start := starts[owner.node.StartLine-1]
		end := min(len(content), starts[owner.node.EndLine-1]+len(sourceLines[owner.node.EndLine-1]))
		opening, parentheses := -1, 0
		for position := start; position < end; position++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			switch masked[position] {
			case '(':
				parentheses++
			case ')':
				parentheses--
			case '{':
				if parentheses == 0 {
					opening = position
				}
			}
			if opening >= 0 {
				break
			}
		}
		if opening < 0 {
			continue
		}
		add(owner.node.SourceSymbolID+":header", "type-header", owner.node.StartLine, lineAt(opening), i)
		children := map[int]javaSymbol{}
		for _, candidate := range symbols {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if candidate.node.SourceSymbolID == owner.node.SourceSymbolID || candidate.node.StartLine <= lineAt(opening) || candidate.node.EndLine >= owner.node.EndLine {
				continue
			}
			nested := false
			for _, parent := range symbols {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if parent.node.Kind != 4 && parent.node.SourceSymbolID != owner.node.SourceSymbolID && parent.node.SourceSymbolID != candidate.node.SourceSymbolID && parent.node.StartLine > owner.node.StartLine && parent.node.EndLine < owner.node.EndLine && parent.node.StartLine <= candidate.node.StartLine && parent.node.EndLine >= candidate.node.EndLine {
					nested = true
					break
				}
			}
			if !nested {
				position := starts[candidate.node.StartLine-1]
				if _, exists := children[position]; !exists {
					children[position] = candidate
				}
			}
		}
		segment := opening + 1
		braces, brackets, initializer := 0, 0, 0
		parentheses = 0
		for position := segment; position < end; position++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if child, exists := children[position]; exists && braces == 0 && parentheses == 0 {
				position = min(end, starts[child.node.EndLine-1]+len(sourceLines[child.node.EndLine-1]))
				segment = position + 1
				continue
			}
			character := masked[position]
			switch character {
			case '(':
				parentheses++
			case ')':
				parentheses--
			case '[':
				brackets++
			case ']':
				brackets--
			case '{':
				braces++
			case '}':
				braces--
			}
			if braces < 0 {
				add(owner.node.SourceSymbolID+":footer", "type-footer", lineAt(position), lineAt(position), i)
				break
			}
			blockEnd := character == '}' && braces == 0 && parentheses == 0 && brackets == 0
			next := skipJavaSpace(masked, position+1, end)
			if character != ';' && (!blockEnd || next < end && (masked[next] == ';' || masked[next] == ',')) {
				continue
			}
			if braces != 0 || parentheses != 0 || brackets != 0 {
				continue
			}
			segment = skipJavaSpace(content, segment, position+1)
			if segment > position {
				segment = position + 1
				continue
			}
			declaration := mrAnnotationPattern.ReplaceAllString(sanitizeJava(content[segment:position+1]), "")
			prefix := strings.TrimRight(declaration, ";")
			if assignment := strings.IndexByte(declaration, '='); assignment >= 0 {
				prefix = declaration[:assignment]
			}
			name := mrFieldNamePattern.FindStringSubmatch(strings.TrimSpace(prefix))
			memberKey, kind := "", "declaration"
			if len(name) > 0 {
				memberKey = "field:" + name[1]
				if !blockEnd {
					kind = "field"
				}
			} else {
				initializer++
				memberKey = fmt.Sprintf("member:%d", initializer)
			}
			add(owner.node.SourceSymbolID+":"+memberKey, kind, lineAt(segment), lineAt(position), i)
			segment = position + 1
		}
	}
	imports := mrImportsPattern.FindAllStringIndex(masked, -1)
	if len(imports) > 0 {
		first := skipJavaSpace(content, imports[0][0], len(content))
		add("imports", "imports", lineAt(first), lineAt(imports[len(imports)-1][1]-1), -1)
	}
	slices.SortStableFunc(result, func(a, b javaUnit) int {
		if a.source.StartLine != b.source.StartLine {
			return a.source.StartLine - b.source.StartLine
		}
		return b.source.EndLine - a.source.EndLine
	})
	merged := []javaUnit{}
	for _, unit := range result {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(merged) > 0 && unit.source.StartLine <= merged[len(merged)-1].source.EndLine {
			prior := &merged[len(merged)-1]
			last := max(prior.source.EndLine, unit.source.EndLine)
			prior.kind = "declaration"
			prior.source.EndLine, prior.source.SymbolEndLine = last, last
			prior.source.Content = strings.Join(sourceLines[prior.source.StartLine-1:last], "\n")
		} else {
			merged = append(merged, unit)
		}
	}
	return merged, ctx.Err()
}

func skipJavaSpace(content string, position, end int) int {
	for position < end {
		r, size := utf8.DecodeRuneInString(content[position:end])
		if !unicode.IsSpace(r) {
			break
		}
		position += size
	}
	return position
}
