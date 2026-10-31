package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
)

func TestBundleKeepsSharedGraphOutOfPlanStateAndArchiveWorks(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); okModel(w) }))
	defer server.Close()
	cfg, route := routeFor(t, server.URL, "")
	doc := document()
	marker := strings.Repeat("shared-signature", 400)
	doc.Graph.Nodes = []review.GraphNode{{ID: "unchanged", SourceSymbolID: "unchanged-source", Path: "other.go", Name: "unused", Signature: marker, Revision: review.Head, StartLine: 1, EndLine: 1}}
	plan, err := BuildDocument(doc, route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := OpenBundle(filepath.Join(t.TempDir(), "bundle"), "input-identity")
	if err != nil {
		t.Fatal(err)
	}
	if err = bundle.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	loaded, err := bundle.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RunID != plan.RunID || loaded.Prepared.Batches[0].UserPrompt != plan.Prepared.Batches[0].UserPrompt {
		t.Fatal("plan identity or model prompt changed")
	}
	directory := filepath.Join(bundle.Directory, "execution")
	result, err := Run(context.Background(), loaded, cfg, directory)
	if err != nil || result.Status != "complete" {
		t.Fatalf("run: %v %+v", err, result)
	}
	if err = bundle.SaveResult(result); err != nil {
		t.Fatal(err)
	}
	archive, err := bundle.ExportArchive(loaded, directory)
	if err != nil || archive.Status != "complete" || calls.Load() != 1 {
		t.Fatalf("archive: %v %+v", err, archive)
	}
	for _, name := range []string{"plan.json", "execution/state.json"} {
		raw, err := os.ReadFile(filepath.Join(bundle.Directory, name))
		if err != nil || bytes.Contains(raw, []byte(marker)) {
			t.Fatal("shared graph copied into", name, err)
		}
	}
	raw, _ := json.Marshal(archive)
	if bytes.Contains(raw, []byte(marker)) || bytes.Contains(raw, []byte("package changed")) {
		t.Fatal("archive repeated prepared graph or source")
	}
	if _, err = bundle.ExportArchive(loaded, directory); err != nil || calls.Load() != 1 {
		t.Fatal("offline export repeated inference", err)
	}
	if _, err = OpenBundle(bundle.Directory, "changed-input"); err == nil {
		t.Fatal("changed invocation was accepted")
	}
}

func TestBundleUnknownRequestCanOnlyRecoverOffline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); okModel(w) }))
	defer server.Close()
	_, route := routeFor(t, server.URL, "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := OpenBundle(filepath.Join(t.TempDir(), "bundle"), "input")
	if err != nil {
		t.Fatal(err)
	}
	if err = bundle.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	plan, err = bundle.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(bundle.Directory, "execution")
	store, err := runstore.OpenWithCodec(dir, bundle.Manifest.PlanHash, bundle)
	if err != nil {
		t.Fatal(err)
	}
	workID, requestID := UUID(), UUID()
	now := time.Now().UTC()
	saved := state{StartedAt: now, Works: []work{{ID: workID, Role: "primary", Prepared: plan.Prepared.Batches[0], Attempts: 1, InFlight: true}}, ArchiveAttempts: []ArchiveAttempt{{WorkID: workID, Number: 1, Phase: "review", RequestID: requestID, Code: "started", StartedAt: now, OutcomeUnknown: true, Schema: review.ResponseSchema()}}}
	if err = store.Write(saved); err != nil {
		t.Fatal(err)
	}
	store.Close()
	resume, err := bundle.CanResume(plan, dir)
	if err != nil || resume {
		t.Fatal("unknown inference was allowed to resume", err)
	}
	archive, err := bundle.ExportArchive(plan, dir)
	if err != nil || archive.Status != "unknown" || calls.Load() != 0 {
		t.Fatal("unknown inference was lost or repeated", err)
	}
	var attempt map[string]json.RawMessage
	if err = json.Unmarshal(archive.Attempts[0], &attempt); err != nil {
		t.Fatal(err)
	}
	var actual string
	_ = json.Unmarshal(attempt["requestId"], &actual)
	if actual != requestID {
		t.Fatal("physical request identity changed")
	}
}

func TestBundleSharedDocumentCrossesLegacyBoundsWithoutGraphCopies(t *testing.T) {
	for _, multiplier := range []int{1, 2} {
		t.Run(strconv.Itoa(multiplier), func(t *testing.T) {
			_, route := routeFor(t, "http://127.0.0.1:1", "")
			doc := document()
			signature := strings.Repeat("signature", 4096)
			for i := 0; i < 2048*multiplier; i++ {
				id := strconv.Itoa(i)
				doc.Graph.Nodes = append(doc.Graph.Nodes, review.GraphNode{ID: "node" + id, SourceSymbolID: "source" + id, Path: "unrelated.go", Name: "unused", Signature: signature, Revision: review.Head, StartLine: 1, EndLine: 1})
			}
			plan, err := BuildDocument(doc, route, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := OpenBundle(filepath.Join(t.TempDir(), "bundle"), "scale-input")
			if err != nil {
				t.Fatal(err)
			}
			if err = bundle.SavePlan(plan); err != nil {
				t.Fatal(err)
			}
			ref := bundle.Manifest.Entries["document"]
			t.Logf("logicalBytes=%d parts=%d distinctNodes=%d", ref.ByteLength, len(ref.Parts), len(doc.Graph.Nodes))
			if ref.ByteLength <= int64(multiplier)*runstore.MaximumDocumentBytes || len(ref.Parts) < 2 {
				t.Fatalf("fixture did not cross the legacy bound: %d", ref.ByteLength)
			}
			loaded, err := bundle.LoadPlan()
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Document.Graph.Nodes) != 2048*multiplier || loaded.Document.Graph.Nodes[len(loaded.Document.Graph.Nodes)-1].Signature != signature || loaded.Prepared.Batches[0].InputSha256 != plan.Prepared.Batches[0].InputSha256 {
				t.Fatal("large shared source or model input changed")
			}
			saved := state{StartedAt: time.Now().UTC(), Works: []work{}}
			for i := 0; i < 12; i++ {
				saved.Works = append(saved.Works, work{ID: UUID(), Role: "primary", Prepared: loaded.Prepared.Batches[0]})
			}
			raw, err := bundle.Encode(saved)
			if err != nil || len(raw) > 100000 || bytes.Contains(raw, []byte(signature)) {
				t.Fatal("state expanded the shared graph", err)
			}
			var restored state
			if err = bundle.Decode(raw, &restored); err != nil || len(restored.Works) != 12 {
				t.Fatal("shared references did not survive state reload", err)
			}
		})
	}
}
