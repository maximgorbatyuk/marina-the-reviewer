package review

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

//go:embed assets/*
var assets embed.FS

// ResponseSchema returns a copy so callers cannot modify the embedded output contract.
func ResponseSchema() json.RawMessage {
	b, _ := assets.ReadFile("assets/response-schema.json")
	return json.RawMessage(b)
}

// DefaultSystemPrompt returns the embedded review instructions used when no override is supplied.
func DefaultSystemPrompt() string {
	p, _ := assets.ReadFile("assets/architecture-prompt.txt")
	return SystemPrompt(string(p))
}

// SystemPrompt appends evidence protocol instructions to the caller's review policy.
func SystemPrompt(policy string) string {
	p, _ := assets.ReadFile("assets/protocol.txt")
	return strings.TrimRight(policy, " \t\r\n") + "\n\n" + string(p)
}

// Hash creates deterministic content identities for persisted and transmitted review data.
func Hash(value string) string { b := sha256.Sum256([]byte(value)); return hex.EncodeToString(b[:]) }
func marshal(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
}
func canonicalJSON(v any) string { return string(marshal(v)) }

type ordinalUnits struct {
	value string
	low   uint16
}

func (units *ordinalUnits) next() (uint16, bool) {
	if units.low != 0 {
		value := units.low
		units.low = 0
		return value, true
	}
	if units.value == "" {
		return 0, false
	}
	first := units.value[0]
	if first < utf8.RuneSelf {
		units.value = units.value[1:]
		return uint16(first), true
	}
	value, width := utf8.DecodeRuneInString(units.value)
	units.value = units.value[width:]
	if value <= 0xffff {
		return uint16(value), true
	}
	value -= 0x10000
	units.low = uint16(value&0x3ff) + 0xdc00
	return uint16(value>>10) + 0xd800, true
}
func ordinalCompare(a, b string) int {
	if a == b {
		return 0
	}
	left, right := ordinalUnits{value: a}, ordinalUnits{value: b}
	for {
		a, hasA := left.next()
		b, hasB := right.next()
		if !hasA {
			if !hasB {
				return 0
			}
			return -1
		}
		if !hasB {
			return 1
		}
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
	}
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
	valueCount, _ := calibratedContext(context.Background(), value)
	return valueCount
}
func calibratedContext(ctx context.Context, value string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var steps uint32
	ascii, other := 0, 0
	for _, r := range value {
		steps++
		if steps&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return int(math.Ceil((float64(ascii)/4 + float64(other)) * 1.5)), nil
}

// EstimateTokens provides a conservative estimate when no provider tokenizer is available.
func EstimateTokens(system, user string, schema json.RawMessage) int {
	return calibrated(system) + calibrated(user) + 256 + calibrated(string(schema)) + 128
}

// CountTokens uses a background context for callers without cancellation support.
func CountTokens(system, user string, schema json.RawMessage) TokenCount {
	count, _ := CountTokensContext(context.Background(), system, user, schema)
	return count
}

// CountTokensContext records estimation limits so callers do not mistake estimates for exact token counts.
func CountTokensContext(ctx context.Context, system, user string, schema json.RawMessage) (TokenCount, error) {
	systemTokens, err := calibratedContext(ctx, system)
	if err != nil {
		return TokenCount{}, err
	}
	userTokens, err := calibratedContext(ctx, user)
	if err != nil {
		return TokenCount{}, err
	}
	schemaTokens, err := calibratedContext(ctx, string(schema))
	if err != nil {
		return TokenCount{}, err
	}
	return TokenCount{TemplatedInputTokens: systemTokens + userTokens + 256, SchemaReserveTokens: schemaTokens + 128, Method: "estimate:unicode-aware-1.5x+256-chat-overhead", Limitation: "No verified provider tokenizer is available. The estimate uses a 1.5 safety factor and relies on bounded context-overflow replanning.", SchemaTokenizationMethod: "estimate:unicode-aware-1.5x+128-wrapper"}, nil
}

// FindingID derives a stable finding identity from validated run and evidence identities.
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

// Fingerprint derives a comparison key from the rule, invariant, and supplied evidence.
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
