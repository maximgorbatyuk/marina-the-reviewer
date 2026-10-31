package review

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

func readLanguageNeutralFunctions(source SourceSnapshot) []javaSymbol {
	symbols, _ := readLanguageNeutralFunctionsContext(context.Background(), source)
	return symbols
}
func readLanguageNeutralFunctionsContext(ctx context.Context, source SourceSnapshot) ([]javaSymbol, error) {
	content := strings.ReplaceAll(source.Content, "\r\n", "\n")
	masked, err := sanitizeJavaContext(ctx, content)
	if err != nil {
		return nil, err
	}
	rawTokens, err := javaTokensContext(ctx, masked)
	if err != nil {
		return nil, err
	}
	tokens, err := declarationTokensContext(ctx, rawTokens)
	if err != nil {
		return nil, err
	}
	result, _, _, err := readMRJavaContext(ctx, source)
	if err != nil {
		return nil, err
	}
	result, err = appendLanguageNeutralMembersContext(ctx, source, tokens, result)
	if err != nil {
		return nil, err
	}
	for i, token := range tokens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if token.text != "function" && token.text != "func" {
			continue
		}
		next := i + 1
		if token.text == "func" && next < len(tokens) && tokens[next].text == "(" {
			close := matchingWithContext(ctx, tokens, next, "(", ")")
			if close < 0 {
				continue
			}
			next = close + 1
		}
		if next < len(tokens) && tokens[next].text == "*" {
			next++
		}
		if next >= len(tokens) || !javaIdentifier(tokens[next].text) {
			continue
		}
		name := tokens[next].text
		next++
		if next < len(tokens) && tokens[next].text == "<" {
			close := matchingWithContext(ctx, tokens, next, "<", ">")
			if close < 0 {
				continue
			}
			next = close + 1
		}
		if next >= len(tokens) || tokens[next].text != "(" {
			continue
		}
		close := matchingWithContext(ctx, tokens, next, "(", ")")
		if close < 0 {
			continue
		}
		opening := close + 1
		for opening < len(tokens) && tokens[opening].text != "{" && tokens[opening].text != ";" {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			opening++
		}
		if opening >= len(tokens) || tokens[opening].text != "{" {
			continue
		}
		end := matchingWithContext(ctx, tokens, opening, "{", "}")
		if end < 0 {
			continue
		}
		start := declarationStartWithContext(ctx, tokens, i)
		prefix := []string{}
		for _, prior := range tokens[start:i] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			prefix = append(prefix, prior.text)
		}
		if !slices.Contains([]string{"", "export", "export default", "async", "export async", "export default async"}, strings.Join(prefix, " ")) {
			continue
		}
		node := GraphNode{Path: source.Path, Name: name, Kind: 4, Revision: source.Revision, StartLine: tokens[start].line, EndLine: tokens[end].line}
		symbol := javaSymbol{node: node, content: sourceSlice(source, node.StartLine, node.EndLine, true).Content, startToken: start, openToken: opening, endToken: end}
		normalizeMRSymbol(&symbol)
		symbol.node.SourceSymbolID = Hash(fmt.Sprintf("%d|%s|%s|%d", source.Revision, source.Path, symbol.key, node.StartLine))
		result = append(result, symbol)
	}
	return result, ctx.Err()
}

func appendLanguageNeutralMembers(source SourceSnapshot, tokens []javaToken, symbols []javaSymbol) []javaSymbol {
	result, _ := appendLanguageNeutralMembersContext(context.Background(), source, tokens, symbols)
	return result
}
func appendLanguageNeutralMembersContext(ctx context.Context, source SourceSnapshot, tokens []javaToken, symbols []javaSymbol) ([]javaSymbol, error) {
	type memberRange struct {
		name        string
		first, last int
	}
	existing := map[memberRange]bool{}
	for _, symbol := range symbols {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if symbol.node.Kind == 4 {
			existing[memberRange{symbol.node.Name, symbol.node.StartLine, symbol.node.EndLine}] = true
		}
	}
	owners := append([]javaSymbol{}, symbols...)
	for _, owner := range owners {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if owner.node.Kind == 4 {
			continue
		}
		depth := 0
		for i := owner.openToken + 1; i < owner.endToken; i++ {
			if err := ctx.Err(); err != nil {
				return nil, err
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
			if isJavaInvocationKeyword(name) {
				continue
			}
			close := matchingWithContext(ctx, tokens, i, "(", ")")
			if close < 0 {
				continue
			}
			start := declarationStartWithContext(ctx, tokens, i-1)
			invalid, abstract := false, false
			for _, prefix := range tokens[start : i-1] {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				invalid = invalid || slices.Contains([]string{"=", "new", "return", "=>", "class", "interface", "record", "enum"}, prefix.text)
				abstract = abstract || prefix.text == "abstract"
			}
			if invalid {
				i = close
				continue
			}
			opening := close + 1
			if opening < len(tokens) && tokens[opening].text == ":" {
				opening++
				if opening < len(tokens) && tokens[opening].text == "{" {
					end := matchingWithContext(ctx, tokens, opening, "{", "}")
					if end < 0 {
						continue
					}
					opening = end + 1
				}
			}
			for opening < owner.endToken && tokens[opening].text != "{" && tokens[opening].text != ";" {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				opening++
			}
			if opening >= owner.endToken || tokens[opening].text == ";" && owner.node.Kind != 1 && !abstract {
				continue
			}
			end := opening
			if tokens[opening].text == "{" {
				end = matchingWithContext(ctx, tokens, opening, "{", "}")
				if end < 0 {
					continue
				}
			}
			first, last := tokens[start].line, tokens[end].line
			key := memberRange{name, first, last}
			if !existing[key] {
				ownerName := owner.node.Name
				symbol := javaSymbol{node: GraphNode{Path: source.Path, Revision: source.Revision, Kind: 4, Name: name, DeclaringType: &ownerName, StartLine: first, EndLine: last}, content: sourceSlice(source, first, last, true).Content, startToken: start, openToken: opening, endToken: end}
				normalizeMRSymbol(&symbol)
				symbol.node.SourceSymbolID = Hash(fmt.Sprintf("%d|%s|%s|%d", source.Revision, source.Path, symbol.key, first))
				symbols = append(symbols, symbol)
				existing[key] = true
			}
			i = end
		}
	}
	return symbols, ctx.Err()
}
