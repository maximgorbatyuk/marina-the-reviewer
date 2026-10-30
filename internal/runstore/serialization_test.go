package runstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicJSONCompactsOversizedLayoutWithinDocumentLimit(t *testing.T) {
	rows := make([][]int, 600000)
	row := make([]int, 16)
	for index := range rows {
		rows[index] = row
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := AtomicJSON(path, rows); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > MaximumDocumentBytes {
		t.Fatalf("compact document: %v", err)
	}
	if bytes.Count(data, []byte{'\n'}) != 1 {
		t.Fatal("oversized layout was not compacted")
	}
	var decoded [][]int
	if err = json.Unmarshal(data, &decoded); err != nil || len(decoded) != len(rows) || len(decoded[0]) != len(row) || len(decoded[len(decoded)-1]) != len(row) {
		t.Fatal("compact write changed the JSON value")
	}
	resumed := filepath.Join(filepath.Dir(path), "resumed.json")
	if err = AtomicJSON(resumed, decoded); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := os.ReadFile(resumed)
	if err != nil || !bytes.Equal(data, roundTrip) {
		t.Fatal("compact document bytes changed after decoding and saving")
	}
}

func TestAtomicJSONKeepsSmallLayoutAndPreservesOutputOnOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	if err := AtomicJSON(path, map[string]int{"value": 1}); err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(previous, []byte("\n  \"value\": 1\n")) {
		t.Fatal("ordinary JSON layout changed")
	}
	if err = AtomicJSON(path, strings.Repeat("x", MaximumDocumentBytes)); err == nil {
		t.Fatal("oversized compact document was accepted")
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, previous) {
		t.Fatal("oversized document replaced the prior output")
	}
}

func TestExecutionStoreKeepsLegacyStateBytes(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "run")
	path := filepath.Join(directory, "state.json")
	raw := json.RawMessage(`{"attempts":1}`)
	if err := AtomicJSON(path, envelope{Version: 1, PlanHash: "plan", State: raw, StateSha256: stateHash(raw)}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(directory, "plan")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var value map[string]int
	if err = store.Read(&value); err != nil || value["attempts"] != 1 {
		t.Fatalf("legacy state: %v", err)
	}
	if err = store.Write(value); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy state encoding changed")
	}
}

func TestAtomicJSONLimitCompactsAndPreservesItsBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution.json")
	if err := AtomicJSONLimit(path, make([]int, 8), 20); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil || len(before) > 20 || bytes.Count(before, []byte{'\n'}) != 1 {
		t.Fatal("bounded writer did not compact the layout")
	}
	if err = AtomicJSONLimit(path, strings.Repeat("x", 20), 20); err == nil {
		t.Fatal("bounded writer accepted an oversized value")
	}
	if err = AtomicJSONLimit(path, "value", 0); err == nil {
		t.Fatal("bounded writer accepted a zero limit")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected value replaced the existing output")
	}
}
