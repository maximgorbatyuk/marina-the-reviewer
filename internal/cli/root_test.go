package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestVersionHasMachineIdentity(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"--version", "--json"}, &bytes.Buffer{}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["tool"] != "marina-the-reviewer" || v["command"] != "marinadoreview" {
		t.Fatalf("wrong identity: %v", v)
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"missing-command"}, &bytes.Buffer{}, &out, &errOut)
	if code != 2 {
		t.Fatalf("want usage exit 2, got %d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
}
