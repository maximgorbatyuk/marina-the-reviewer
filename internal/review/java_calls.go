package review

import (
	"regexp"
	"strings"
)

var javaVariablePattern = regexp.MustCompile(`\b([A-Za-z_$][\w.$]*)(?:\s*<[^;={}()]+>)?(?:\s*\[\])?\s+([A-Za-z_$][\w$]*)\s*[=;,)]`)

func variableTypes(source string) map[string]string {
	out := map[string]string{}
	for _, m := range javaVariablePattern.FindAllStringSubmatch(sanitizeJava(source), -1) {
		name := m[1]
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		switch name {
		case "return", "new", "throw", "public", "private", "protected", "static", "final":
			continue
		}
		out[m[2]] = name
	}
	return out
}
func invocationArity(source string, opening int) int {
	if opening < 0 || opening >= len(source) || source[opening] != '(' {
		return -1
	}
	level, brackets, braces, commas := 0, 0, 0, 0
	nonempty := false
	quote := byte(0)
	blockComment, lineComment, textBlock, escaped := false, false, false, false
	for i := opening + 1; i < len(source); i++ {
		c := source[i]
		if lineComment {
			if c == '\n' {
				lineComment = false
			}
			continue
		}
		if blockComment {
			if c == '*' && i+1 < len(source) && source[i+1] == '/' {
				i++
				blockComment = false
			}
			continue
		}
		if textBlock {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' && i+2 < len(source) && source[i:i+3] == `"""` {
				i += 2
				textBlock = false
			}
			continue
		}
		if quote != 0 {
			if c == '\n' || c == '\r' {
				return -1
			}
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '/' && i+1 < len(source) {
			if source[i+1] == '/' {
				lineComment = true
				i++
				continue
			}
			if source[i+1] == '*' {
				blockComment = true
				i++
				continue
			}
		}
		if c == '"' && i+2 < len(source) && source[i:i+3] == `"""` {
			nonempty = true
			textBlock = true
			i += 2
			continue
		}
		if c == '"' || c == '\'' {
			nonempty = true
			quote = c
			continue
		}
		switch c {
		case '(':
			level++
		case ')':
			if level == 0 {
				if brackets != 0 || braces != 0 {
					return -1
				}
				if !nonempty {
					return 0
				}
				return commas + 1
			}
			level--
		case '[':
			brackets++
		case ']':
			brackets--
		case '{':
			braces++
		case '}':
			braces--
		case ',':
			if level == 0 && brackets == 0 && braces == 0 {
				commas++
			}
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			nonempty = true
		}
	}
	return -1
}
