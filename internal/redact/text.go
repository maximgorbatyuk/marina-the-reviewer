package redact

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

func Text(value string, tokens map[string]string) string {
	secrets := []string{}
	for _, token := range tokens {
		if token != "" {
			secrets = append(secrets, token)
		}
	}
	if len(secrets) == 0 {
		return value
	}
	spans := tokenSpans(value, secrets, nil, nil)
	decode := strings.Contains(value, `\u`)
	for _, secret := range secrets {
		decode = decode || strings.ContainsAny(secret, "\"\\/\b\f\n\r\t")
	}
	if decode {
		decoded, starts, ends := decodeTextSpans(value)
		for {
			spans = append(spans, tokenSpans(decoded, secrets, starts, ends)...)
			unescaped, localStarts, localEnds := decodeTextSpans(decoded)
			if unescaped == decoded {
				break
			}
			for i := range localStarts {
				localStarts[i], localEnds[i] = starts[localStarts[i]], ends[localEnds[i]-1]
			}
			decoded, starts, ends = unescaped, localStarts, localEnds
		}
	}
	if len(spans) == 0 {
		return value
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var output strings.Builder
	copied := 0
	for _, span := range spans {
		if span.end <= copied {
			continue
		}
		if span.start >= copied {
			output.WriteString(value[copied:span.start])
			output.WriteString("[REDACTED]")
		}
		copied = span.end
	}
	output.WriteString(value[copied:])
	return output.String()
}

type redactionSpan struct{ start, end int }

func tokenSpans(value string, secrets []string, starts, ends []int) []redactionSpan {
	spans := []redactionSpan{}
	for _, secret := range secrets {
		position := 0
		for {
			index := strings.Index(value[position:], secret)
			if index < 0 {
				break
			}
			start := position + index
			end := start + len(secret)
			position = end
			if starts != nil {
				start, end = starts[start], ends[end-1]
			}
			spans = append(spans, redactionSpan{start, end})
		}
	}
	return spans
}

func decodeTextSpans(value string) (string, []int, []int) {
	var decoded strings.Builder
	starts, ends := []int{}, []int{}
	for position := 0; position < len(value); {
		start := position
		character, size := utf8.DecodeRuneInString(value[position:])
		position += size
		if character == '\\' && position < len(value) {
			next := value[position]
			if next == 'u' && position+5 <= len(value) {
				if number, err := strconv.ParseUint(value[position+1:position+5], 16, 16); err == nil {
					character = rune(number)
					position += 5
					if utf16.IsSurrogate(character) && position+6 <= len(value) && value[position:position+2] == `\u` {
						if low, err := strconv.ParseUint(value[position+2:position+6], 16, 16); err == nil {
							if combined := utf16.DecodeRune(character, rune(low)); combined != utf8.RuneError {
								character = combined
								position += 6
							}
						}
					}
				}
			} else if replacement, ok := map[byte]rune{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}[next]; ok {
				character = replacement
				position++
			}
		}
		text := string(character)
		decoded.WriteString(text)
		for range len(text) {
			starts = append(starts, start)
			ends = append(ends, position)
		}
	}
	return decoded.String(), starts, ends
}
