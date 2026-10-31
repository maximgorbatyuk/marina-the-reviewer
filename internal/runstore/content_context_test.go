package runstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func contentTestPut(ctx context.Context, store *ContentStore, input io.Reader) (ContentRef, error) {
	return store.PutContext(ctx, input)
}
func contentTestPutJSON(ctx context.Context, store *ContentStore, value any) (ContentRef, error) {
	return store.PutJSONContext(ctx, value)
}
func contentTestReader(ctx context.Context, store *ContentStore, ref ContentRef) (io.ReadCloser, error) {
	return store.ReaderContext(ctx, ref)
}
func contentTestDecode(ctx context.Context, store *ContentStore, ref ContentRef, value any) error {
	return store.DecodeContext(ctx, ref, value)
}
func contentTestAtomic(ctx context.Context, path string, raw []byte) error {
	return AtomicBytesContext(ctx, path, raw)
}
func contentTestAtomicJSON(ctx context.Context, path string, value any, maximum int) error {
	return AtomicJSONLimitContext(ctx, path, value, maximum)
}

type cancelContentReader struct {
	raw                        []byte
	position, threshold, calls int
	cancel                     context.CancelFunc
}

func (r *cancelContentReader) Read(out []byte) (int, error) {
	r.calls++
	if r.position == len(r.raw) {
		return 0, io.EOF
	}
	count := min(len(out), 8192, len(r.raw)-r.position)
	copy(out, r.raw[r.position:r.position+count])
	r.position += count
	if r.position >= r.threshold {
		r.cancel()
	}
	return count, nil
}

type contentFenceContext struct {
	context.Context
	cancel   context.CancelFunc
	parent   string
	expected int64
	mu       sync.Mutex
	fenced   bool
}

func (c *contentFenceContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Context.Err(); err != nil {
		return err
	}
	entries, _ := os.ReadDir(c.parent)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".marinadoreview-") {
			if info, err := entry.Info(); err == nil && info.Size() == c.expected {
				c.fenced = true
				c.cancel()
				break
			}
		}
	}
	return c.Context.Err()
}
func contentCommitFence(parent string, expected int64) *contentFenceContext {
	ctx, cancel := context.WithCancel(context.Background())
	return &contentFenceContext{Context: ctx, cancel: cancel, parent: parent, expected: expected}
}

type cancelJSONValue struct{ cancel context.CancelFunc }

func (value cancelJSONValue) MarshalJSON() ([]byte, error) {
	value.cancel()
	return []byte(`{"value":"complete"}`), nil
}

func contextStore(t *testing.T) *ContentStore {
	t.Helper()
	store, err := NewContentStore(filepath.Join(t.TempDir(), "bundle"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func noContentTemps(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".marinadoreview-") {
			t.Fatalf("cancelled temporary file was retained: %s", entry.Name())
		}
	}
}

func TestContentContextCancellationDuringPartDoesNotCommitIt(t *testing.T) {
	store := contextStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &cancelContentReader{raw: bytes.Repeat([]byte("x"), MaximumContentPartBytes), threshold: 16384, cancel: cancel}
	if _, err := contentTestPut(ctx, store, reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-part cancellation returned %v", err)
	}
	if reader.calls > 2 {
		t.Fatalf("cancelled source kept reading: %d calls", reader.calls)
	}
	entries, _ := os.ReadDir(filepath.Join(store.Directory, "content"))
	if len(entries) != 0 {
		t.Fatal("cancelled current part was committed")
	}
	noContentTemps(t, filepath.Join(store.Directory, "content"))
}

func TestContentContextCancellationKeepsCompletedImmutableParts(t *testing.T) {
	store := contextStore(t)
	retained, err := store.PutText("previous artifact")
	if err != nil {
		t.Fatal(err)
	}
	first := bytes.Repeat([]byte("a"), MaximumContentPartBytes)
	raw := append(append([]byte{}, first...), bytes.Repeat([]byte("b"), 32768)...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &cancelContentReader{raw: raw, threshold: MaximumContentPartBytes + 16384, cancel: cancel}
	if _, err = contentTestPut(ctx, store, reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("second part cancellation returned %v", err)
	}
	sum := sha256.Sum256(first)
	path := filepath.Join(store.Directory, "content", hex.EncodeToString(sum[:])+".bin")
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, first) {
		t.Fatal("completed immutable first part was lost", err)
	}
	previous, err := store.Bytes(retained)
	if err != nil || string(previous) != "previous artifact" {
		t.Fatal("earlier artifact was damaged", err)
	}
	reopened, err := NewContentStore(store.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.partCount != 2 || store.partCount != 2 || reopened.uniqueBytes != store.uniqueBytes {
		t.Fatalf("cancelled part changed resource accounting: reopened%+v current%+v", reopened, store)
	}
	noContentTemps(t, filepath.Join(store.Directory, "content"))
}

func TestAtomicContextCommitFencePreservesExistingOutput(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "artifact.json")
	old := []byte("existing output")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("replacement output with different length")
	ctx := contentCommitFence(parent, int64(len(replacement)))
	defer ctx.cancel()
	if err := contentTestAtomic(ctx, path, replacement); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-rename cancellation returned %v", err)
	}
	if !ctx.fenced {
		t.Fatal("test did not reach the completed temporary file fence")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, old) {
		t.Fatal("cancellation replaced the previous output", err)
	}
	noContentTemps(t, parent)
}

func TestContentContextReaderAndDecoderObserveCancellation(t *testing.T) {
	store := contextStore(t)
	raw := bytes.Repeat([]byte("r"), MaximumContentPartBytes+10)
	ref, err := store.Put(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader, err := contentTestReader(ctx, store, ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Read(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if n, err := reader.Read(make([]byte, 1024)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reader returned n=%d err=%v", n, err)
	}
	if err = reader.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled close drained content: %v", err)
	}
	jsonRef, err := store.PutJSON(map[string]int{"value": 1})
	if err != nil {
		t.Fatal(err)
	}
	target := map[string]int{"retained": 2}
	if err = contentTestDecode(ctx, store, jsonRef, &target); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled decoder returned %v", err)
	}
	if !reflect.DeepEqual(target, map[string]int{"retained": 2}) {
		t.Fatal("cancelled decoder changed its target")
	}
}

func TestContentContextJSONCancellationDoesNotCommitEncodedValue(t *testing.T) {
	store := contextStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := contentTestPutJSON(ctx, store, cancelJSONValue{cancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("encoding cancellation returned %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(store.Directory, "content"))
	if len(entries) != 0 {
		t.Fatal("cancelled encoded value created content parts")
	}
}

func TestAtomicContextJSONCancellationPreservesOutput(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "manifest.json")
	old := []byte("existing manifest")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := contentTestAtomicJSON(ctx, path, cancelJSONValue{cancel}, 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("JSON encoding cancellation returned %v", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, old) {
		t.Fatal("cancelled JSON output changed the saved manifest", err)
	}
	noContentTemps(t, parent)
}

func TestContentContextUncancelledByteSHAAndJSONLayoutIdentity(t *testing.T) {
	store := contextStore(t)
	raw := bytes.Repeat([]byte("exact source\n"), MaximumContentPartBytes/13+33)
	before, err := store.Put(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	after, err := contentTestPut(context.Background(), store, bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("context changed content references", err)
	}
	sum := sha256.Sum256(raw)
	if after.Sha256 != hex.EncodeToString(sum[:]) {
		t.Fatal("context changed exact-byte SHA")
	}
	value := map[string]any{"unicode": "Привет <tag> & world", "raw": json.RawMessage(`{"x":1}`), "null": nil}
	expected, err := store.PutJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := contentTestPutJSON(context.Background(), store, value)
	if err != nil || !reflect.DeepEqual(expected, actual) {
		t.Fatal("context changed JSON content identity", err)
	}
	path := filepath.Join(t.TempDir(), "compact.json")
	if err = contentTestAtomicJSON(context.Background(), path, make([]int, 8), 20); err != nil {
		t.Fatal(err)
	}
	compact, err := os.ReadFile(path)
	if err != nil || string(compact) != "[0,0,0,0,0,0,0,0]\n" {
		t.Fatalf("context changed compact fallback bytes: %q %v", compact, err)
	}
}

func TestContentContextPartCommitFenceKeepsEarlierArtifact(t *testing.T) {
	store := contextStore(t)
	previous, err := store.PutText("retained original")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("new immutable part that must not be published")
	ctx := contentCommitFence(filepath.Join(store.Directory, "content"), int64(len(raw)))
	defer ctx.cancel()
	if _, err = store.PutContext(ctx, bytes.NewReader(raw)); !errors.Is(err, context.Canceled) {
		t.Fatalf("part commit fence returned %v", err)
	}
	if !ctx.fenced {
		t.Fatal("part commit test did not reach its temporary file")
	}
	sum := sha256.Sum256(raw)
	if _, err = os.Stat(filepath.Join(store.Directory, "content", hex.EncodeToString(sum[:])+".bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled immutable part exists: %v", err)
	}
	saved, err := store.Bytes(previous)
	if err != nil || string(saved) != "retained original" {
		t.Fatal("part cancellation changed an earlier artifact", err)
	}
	if store.partCount != 1 || store.uniqueBytes != int64(len(saved)) {
		t.Fatal("cancelled part changed resource counters")
	}
	noContentTemps(t, filepath.Join(store.Directory, "content"))
}

func TestAtomicContextJSONCommitFenceKeepsExistingManifest(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "bundle.json")
	old := []byte("saved manifest")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	value := map[string]int{"new": 1}
	var expected bytes.Buffer
	encoder := json.NewEncoder(&expected)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	ctx := contentCommitFence(parent, int64(expected.Len()))
	defer ctx.cancel()
	if err := AtomicJSONContext(ctx, path, value); !errors.Is(err, context.Canceled) {
		t.Fatalf("manifest commit fence returned %v", err)
	}
	if !ctx.fenced {
		t.Fatal("manifest commit test did not reach its temporary file")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, old) {
		t.Fatal("cancelled manifest replaced the existing one", err)
	}
	noContentTemps(t, parent)
}

func TestContentContextCancelledBeforeOperationDoesNotReadOrWrite(t *testing.T) {
	store := contextStore(t)
	ref, err := store.PutText("retained")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.PutTextContext(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled empty input returned %v", err)
	}
	if _, err = store.BytesContext(ctx, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled content read returned %v", err)
	}
	path := filepath.Join(store.Directory, "untouched.json")
	if err = AtomicJSONContext(ctx, path, map[string]int{"x": 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled atomic JSON returned %v", err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pre-cancelled atomic JSON created an output")
	}
}
