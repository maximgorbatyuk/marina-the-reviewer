package review

import (
	"regexp"
	"strconv"
	"strings"
)

var javaUnicodeEscapePattern = regexp.MustCompile(`^\\u+[0-9a-fA-F]{4}`)

func hasUnsafeJavaUnicodeEscapes(content, masked string) bool {
	for offset := 0; offset < len(content); {
		position := strings.Index(content[offset:], `\u`)
		if position < 0 {
			return false
		}
		position += offset
		match := javaUnicodeEscapePattern.FindStringIndex(content[position:])
		if match == nil || masked[position] != ' ' {
			return true
		}
		end := position + match[1]
		value, err := strconv.ParseUint(content[end-4:end], 16, 16)
		if err != nil || value < 0x20 || strings.ContainsRune("\\\"'/*", rune(value)) {
			return true
		}
		offset = end
	}
	return false
}
