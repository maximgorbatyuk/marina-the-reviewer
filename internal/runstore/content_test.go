package runstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestContentPartsKeepExactBytesAndRejectChangedContent(t *testing.T) {
	store, err := NewContentStore(filepath.Join(t.TempDir(), "bundle"))
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("source\n"), MaximumContentPartBytes/7+19)
	ref, err := store.Put(bytes.NewReader(want))
	if err != nil || len(ref.Parts) != 2 {
		t.Fatalf("parts: %v %+v", err, ref)
	}
	got, err := store.Bytes(ref)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("content bytes changed", err)
	}
	again, err := store.Put(bytes.NewReader(want))
	if err != nil || again.Sha256 != ref.Sha256 {
		t.Fatal("content identity changed", err)
	}
	path := filepath.Join(store.Directory, ref.Parts[0].File)
	if err = os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Bytes(ref); err == nil {
		t.Fatal("changed content was accepted")
	}
}

func TestContentJSONKeepsEncoderBytes(t *testing.T) {
	store, err := NewContentStore(filepath.Join(t.TempDir(), "bundle"))
	if err != nil {
		t.Fatal(err)
	}
	value := map[string]any{"unicode": "Привет <tag> & world", "raw": json.RawMessage(`{"x":1,"y":[2,3]}`), "null": nil}
	var expected bytes.Buffer
	if err = json.NewEncoder(&expected).Encode(value); err != nil {
		t.Fatal(err)
	}
	ref, err := store.PutJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := store.Bytes(ref)
	if err != nil || !bytes.Equal(actual, expected.Bytes()) {
		t.Fatal("bounded metadata encoding changed exact bytes", err)
	}
}

func TestContentResourcePolicyIsTunableAndKeepsExistingParts(t *testing.T) {
	store, err := NewContentStoreWithLimits(filepath.Join(t.TempDir(), "bundle"), ContentLimits{MaximumUniqueBytes: 8, MaximumParts: 1})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.PutText("retained")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PutText("new")
	var limit *ContentLimitError
	if !errors.As(err, &limit) || limit.Actual != 2 || limit.Allowed != 1 {
		t.Fatalf("part limit diagnostic: %v", err)
	}
	actual, err := store.Bytes(ref)
	if err != nil || string(actual) != "retained" {
		t.Fatal("resource failure damaged saved content", err)
	}
	larger, err := NewContentStoreWithLimits(store.Directory, ContentLimits{MaximumUniqueBytes: 11, MaximumParts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = larger.PutText("new"); err != nil {
		t.Fatal("explicit larger resource policy was ignored", err)
	}
}

func TestContentReferenceCannotLeaveItsBundle(t *testing.T) {
	store, err := NewContentStore(filepath.Join(t.TempDir(), "bundle"))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(bytes.NewBufferString("retained"))
	if err != nil {
		t.Fatal(err)
	}
	ref.Parts[0].File = "../outside.bin"
	if _, err = store.Bytes(ref); err == nil {
		t.Fatal("external content reference was accepted")
	}
}
