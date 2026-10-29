package review

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

//go:embed assets/*
var assets embed.FS

func ResponseSchema() json.RawMessage {
	b, _ := assets.ReadFile("assets/response-schema.json")
	return json.RawMessage(b)
}
func DefaultSystemPrompt() string {
	p, _ := assets.ReadFile("assets/architecture-prompt.txt")
	return SystemPrompt(string(p))
}
func SystemPrompt(policy string) string {
	p, _ := assets.ReadFile("assets/protocol.txt")
	return strings.TrimRight(policy, " \t\r\n") + "\n\n" + string(p)
}
func Hash(value string) string { b := sha256.Sum256([]byte(value)); return hex.EncodeToString(b[:]) }
func marshal(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
}
func canonicalJSON(v any) string { return string(marshal(v)) }
func ordinalCompare(a, b string) int {
	return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
}
func unique(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		if !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	return out
}
func calibrated(value string) int {
	ascii, other := 0, 0
	for _, r := range value {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	return int(math.Ceil((float64(ascii)/4 + float64(other)) * 1.5))
}
func EstimateTokens(system, user string, schema json.RawMessage) int {
	return calibrated(system) + calibrated(user) + 256 + calibrated(string(schema)) + 128
}
func CountTokens(system, user string, schema json.RawMessage) TokenCount {
	return TokenCount{TemplatedInputTokens: calibrated(system) + calibrated(user) + 256, SchemaReserveTokens: calibrated(string(schema)) + 128, Method: "estimate:unicode-aware-1.5x+256-chat-overhead", Limitation: "No verified provider tokenizer is available. The estimate uses a 1.5 safety factor and relies on bounded context-overflow replanning.", SchemaTokenizationMethod: "estimate:unicode-aware-1.5x+128-wrapper"}
}
func FindingID(run, pass, fingerprint string) (string, error) {
	normalize := func(v string) (string, error) {
		v = strings.ReplaceAll(v, "-", "")
		if len(v) != 32 {
			return "", fmt.Errorf("identity must contain UUID values")
		}
		if _, e := hex.DecodeString(v); e != nil {
			return "", fmt.Errorf("identity must contain UUID values")
		}
		return strings.ToLower(v), nil
	}
	r, e := normalize(run)
	if e != nil {
		return "", e
	}
	p, e := normalize(pass)
	if e != nil {
		return "", e
	}
	b, _ := hex.DecodeString(Hash(r + "|" + p + "|" + fingerprint)[:32])
	return fmt.Sprintf("%x-%x-%x-%x-%x", []byte{b[3], b[2], b[1], b[0]}, []byte{b[5], b[4]}, []byte{b[7], b[6]}, b[8:10], b[10:16]), nil
}
func Fingerprint(rule, invariant string, evidence []Evidence, blocks map[string]Block) string {
	keys := []string{}
	for _, e := range evidence {
		b := blocks[e.BlockID]
		s := b.Base
		if e.Revision == "head" {
			s = b.Head
		}
		keys = append(keys, s.Path+"|"+e.Revision+"|"+strconv.Itoa(e.StartLine)+"|"+strconv.Itoa(e.EndLine)+"|"+strings.TrimSpace(e.Quote))
	}
	keys = unique(keys)
	slices.SortFunc(keys, ordinalCompare)
	return Hash(rule + "|" + strings.ToUpper(strings.TrimSpace(invariant)) + "|" + strings.Join(keys, "\n"))
}
