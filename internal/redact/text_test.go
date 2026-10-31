package redact

import (
	"encoding/json"
	"testing"
)

func TestTextMasksEscapedKeysWithoutChangingSurroundingBytes(t *testing.T) {
	for _, secret := range []string{"test-key", "test/key", `test\key`, `test"key`, "test\nkey", "test-ключ"} {
		input, want := "before:"+secret+":after", "before:[REDACTED]:after"
		for depth := 0; depth < 4; depth++ {
			if got := Text(input, map[string]string{"provider": secret}); got != want {
				t.Fatalf("key encoding at depth %d changed surrounding bytes", depth)
			}
			encoded, _ := json.Marshal(input)
			input = string(encoded)
			encoded, _ = json.Marshal(want)
			want = string(encoded)
		}
	}
}
