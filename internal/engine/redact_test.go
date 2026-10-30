package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRedactTextKeepsInvalidOutputAndMasksEscapedTokens(t *testing.T) {
	for _, input := range []string{`{"broken":"private-token`, `{"broken":"\u0070rivate-token`, `{"broken":"\u0070\u0072ivate-token`, `{"content":"{\"broken\":\"\\u0070rivate-token`} {
		output := redactText(input, map[string]string{"provider": "private-token"})
		if !strings.Contains(output, "broken") || !strings.Contains(output, "[REDACTED]") || strings.Contains(output, "private-token") || strings.Contains(output, "rivate-token") {
			t.Fatalf("unsafe rejected text: %q", output)
		}
	}
}

func TestRedactTextMasksTokensAtEveryEscapeDepth(t *testing.T) {
	for _, secret := range []string{"token-private-canary", "return-private-canary", "newline-private-canary", "backspace-private-canary", "formfeed-private-canary", "u0041-private-canary"} {
		t.Run(secret[:1], func(t *testing.T) {
			for _, prefix := range []string{secret[:1], fmt.Sprintf(`\u%04x`, secret[0])} {
				input := `{"value":"\\` + prefix + secret[1:] + `"}`
				want := `{"value":"\\[REDACTED]"}`
				for depth := 0; depth < 4; depth++ {
					if got := redactText(input, map[string]string{"provider": secret}); got != want {
						t.Fatalf("depth %d did not mask the token while preserving its surrounding text", depth)
					}
					encoded, _ := json.Marshal(input)
					input = string(encoded)
					encoded, _ = json.Marshal(want)
					want = string(encoded)
				}
			}
		})
	}
}
