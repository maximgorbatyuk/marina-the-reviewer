package review

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

func sanitizeJavaContext(ctx context.Context, s string) (string, error) {
	b := []byte(s)
	quote := byte(0)
	block, lineComment, textBlock, escaped := false, false, false, false
	for i := 0; i < len(b); i++ {
		if i&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
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
	return string(b), ctx.Err()
}

func javaTokensContext(ctx context.Context, s string) ([]javaToken, error) {
	out := []javaToken{}
	line := 1
	for i := 0; i < len(s); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
				if err := ctx.Err(); err != nil {
					return nil, err
				}
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
	return out, ctx.Err()
}

func matchingWithContext(ctx context.Context, tokens []javaToken, start int, open, closing string) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		if i&255 == 0 && ctx.Err() != nil {
			return -1
		}
		if tokens[i].text == open {
			depth++
		}
		if tokens[i].text == closing {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func declarationStartWithContext(ctx context.Context, tokens []javaToken, i int) int {
	for i > 0 {
		if i&255 == 0 && ctx.Err() != nil {
			return i
		}
		p := tokens[i-1].text
		if p == ";" || p == "{" || p == "}" {
			break
		}
		i--
	}
	return i
}

func declarationTokensContext(ctx context.Context, tokens []javaToken) ([]javaToken, error) {
	declarations := make([]javaToken, 0, len(tokens))
	for index := 0; index < len(tokens); index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token := tokens[index]
		if token.text != "@" || index+1 >= len(tokens) || tokens[index+1].text == "interface" || !javaIdentifier(tokens[index+1].text) {
			declarations = append(declarations, token)
			continue
		}
		end := index + 2
		for end+1 < len(tokens) && tokens[end].text == "." && javaIdentifier(tokens[end+1].text) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			end += 2
		}
		if end < len(tokens) && tokens[end].text == "(" {
			closing := matchingWithContext(ctx, tokens, end, "(", ")")
			if closing < 0 {
				declarations = append(declarations, token)
				continue
			}
			end = closing + 1
		}
		// Keep the source span so declaration slices retain annotations without reading their arguments as members.
		token.text, token.end = "", tokens[end-1].end
		declarations = append(declarations, token)
		index = end - 1
	}
	return declarations, ctx.Err()
}

func readJavaWithUnicodePolicyContext(ctx context.Context, s SourceSnapshot, allowInertEscapes bool) ([]javaSymbol, []javaUnit, []string, error) {
	content := strings.ReplaceAll(s.Content, "\r\n", "\n")
	masked, err := sanitizeJavaContext(ctx, content)
	if err != nil {
		return nil, nil, nil, err
	}
	rawTokens, err := javaTokensContext(ctx, masked)
	if err != nil {
		return nil, nil, nil, err
	}
	tokens, err := declarationTokensContext(ctx, rawTokens)
	if err != nil {
		return nil, nil, nil, err
	}
	symbols := []javaSymbol{}
	units := []javaUnit{}
	limitations := []string{}
	if strings.Contains(content, `\u`) && (!allowInertEscapes || hasUnsafeJavaUnicodeEscapes(content, masked)) {
		return symbols, units, []string{s.Path + ": Java Unicode escapes prevent safe declaration analysis."}, ctx.Err()
	}
	kinds := map[string]int{"class": 0, "interface": 1, "record": 2, "enum": 3}
	for i, t := range tokens {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		kind, ok := kinds[t.text]
		if !ok || i+1 >= len(tokens) || !javaIdentifier(tokens[i+1].text) || i > 0 && tokens[i-1].text == "." {
			continue
		}
		opening := -1
		for j := i + 2; j < len(tokens); j++ {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, err
			}
			if tokens[j].text == ";" {
				break
			}
			if tokens[j].text == "(" {
				end := matchingWithContext(ctx, tokens, j, "(", ")")
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
		end := matchingWithContext(ctx, tokens, opening, "{", "}")
		if end < 0 {
			limitations = append(limitations, s.Path+": incomplete Java type body.")
			continue
		}
		start := declarationStartWithContext(ctx, tokens, i)
		name := tokens[i+1].text
		signature := strings.Join(strings.Fields(content[tokens[start].start:tokens[opening].start]), " ")
		node := GraphNode{Path: s.Path, Name: name, Signature: signature, Kind: kind, Revision: s.Revision, StartLine: tokens[start].line, EndLine: tokens[end].line}
		symbols = append(symbols, javaSymbol{node: node, key: "type:" + name, content: sourceSlice(s, node.StartLine, node.EndLine, true).Content, startToken: start, openToken: opening, endToken: end})
	}
	// Limit candidates to direct type members so expressions cannot become methods.
	types := append([]javaSymbol{}, symbols...)
	for _, owner := range types {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		depth := 0
		for i := owner.openToken + 1; i < owner.endToken; i++ {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, err
			}
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
			closing := matchingWithContext(ctx, tokens, i, "(", ")")
			if closing < 0 {
				continue
			}
			next := closing + 1
			if next < len(tokens) && tokens[next].text == "throws" {
				for next < len(tokens) && tokens[next].text != "{" && tokens[next].text != ";" {
					if err := ctx.Err(); err != nil {
						return nil, nil, nil, err
					}
					next++
				}
			}
			if next >= len(tokens) || (tokens[next].text != "{" && tokens[next].text != ";") {
				i = closing
				continue
			}
			start := declarationStartWithContext(ctx, tokens, i-1)
			prefix := tokens[start : i-1]
			bad := false
			for _, p := range prefix {
				if err := ctx.Err(); err != nil {
					return nil, nil, nil, err
				}
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
				end = matchingWithContext(ctx, tokens, next, "{", "}")
				if end < 0 {
					limitations = append(limitations, s.Path+": incomplete Java method body.")
					continue
				}
			}
			parameters := 0
			paramTypes := []string{}
			if closing > i+1 {
				parameters = 1
				level := 0
				segment := i + 1
				for j := i + 1; j <= closing; j++ {
					if err := ctx.Err(); err != nil {
						return nil, nil, nil, err
					}
					v := tokens[j].text
					if v == "<" || v == "(" || v == "[" {
						level++
					}
					if v == ">" || v == ")" || v == "]" {
						level--
					}
					if j == closing || v == "," && level == 0 {
						part := tokens[segment:j]
						if len(part) > 0 {
							typeParts := []string{}
							for _, p := range part[:max(0, len(part)-1)] {
								if err := ctx.Err(); err != nil {
									return nil, nil, nil, err
								}
								if p.text != "final" {
									typeParts = append(typeParts, p.text)
								}
							}
							paramTypes = append(paramTypes, strings.Join(typeParts, ""))
						}
						if j != closing {
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
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		sym := &symbols[i]
		if sym.node.Kind != 4 {
			parent := ""
			for _, t := range types {
				if err := ctx.Err(); err != nil {
					return nil, nil, nil, err
				}
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
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
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
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, err
			}
			childEnd := -1
			for _, child := range symbols {
				if err := ctx.Err(); err != nil {
					return nil, nil, nil, err
				}
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
				end := matchingWithContext(ctx, tokens, j, "{", "}")
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
					if err := ctx.Err(); err != nil {
						return nil, nil, nil, err
					}
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
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, err
			}
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
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		if len(merged) > 0 && u.source.StartLine <= merged[len(merged)-1].source.EndLine {
			last := &merged[len(merged)-1]
			last.kind = "declaration"
			last.source = sourceSlice(s, last.source.StartLine, max(last.source.EndLine, u.source.EndLine), true)
		} else {
			merged = append(merged, u)
		}
	}
	return symbols, merged, limitations, ctx.Err()
}
