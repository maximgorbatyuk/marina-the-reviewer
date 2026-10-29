package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
)

func TestResumeRejectsSupersededParentWithoutCoverage(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer a.Close()
	c, route := routeFor(t, a.URL, "")
	p, e := BuildDocument(document(), route, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "run")
	Run(context.Background(), p, c, dir)
	encoded, _ := json.Marshal(p)
	store, e := runstore.Open(dir, review.Hash(string(encoded)))
	if e != nil {
		t.Fatal(e)
	}
	var saved state
	if e = store.Read(&saved); e != nil {
		t.Fatal(e)
	}
	saved.Works[0].Superseded = true
	saved.Works[0].Failure = ""
	if e = store.Write(saved); e != nil {
		t.Fatal(e)
	}
	store.Close()
	if result, e := Run(context.Background(), p, c, dir); e == nil || result.Status == "complete" {
		t.Fatal("orphan supersession accepted as complete")
	}
}

func TestResumeRejectsReplacementChangedLineLoss(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { okModel(w) }))
	defer b.Close()
	c, route := routeFor(t, a.URL, b.URL)
	p, e := BuildDocument(document(), route, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "run")
	if _, e = Run(context.Background(), p, c, dir); e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(p)
	store, e := runstore.Open(dir, review.Hash(string(encoded)))
	if e != nil {
		t.Fatal(e)
	}
	var saved state
	if e = store.Read(&saved); e != nil {
		t.Fatal(e)
	}
	saved.Works[1].Prepared.Input.Batch.Blocks[0].ChangedLines = []review.ChangedLine{}
	saved.Works[1].Prepared, e = review.RefreshPrepared(saved.Works[1].Prepared)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Write(saved); e != nil {
		t.Fatal(e)
	}
	store.Close()
	if result, e := Run(context.Background(), p, c, dir); e == nil || result.Status == "complete" {
		t.Fatal("replacement lost changed-line ownership")
	}
}

func TestPlanRejectsChangedPreparedSourceAndBudget(t *testing.T) {
	_, route := routeFor(t, "http://127.0.0.1:1", "")
	p, e := BuildDocument(document(), route, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	p.Prepared.Batches[0].Budget.AvailableUserInputTokens++
	if e = p.Validate(); e == nil {
		t.Fatal("forged budget accepted")
	}
}

func TestProviderCannotEchoCredentialIntoSavedArtifacts(t *testing.T) {
	const secret = "private-canary-token"
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"checkedBlockIds":["b1"],"findings":[],"strengths":[],"unconfirmedDependencies":["` + secret + `"]}`}, "finish_reason": "stop"}}})
	}))
	defer a.Close()
	c, r := routeFor(t, a.URL, "")
	r.RuntimeSecrets = map[string]string{r.Primary.Name: secret}
	p, e := BuildDocument(document(), r, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	result, _ := Run(context.Background(), p, c, filepath.Join(t.TempDir(), "run"))
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), secret) {
		t.Fatal("provider echoed token into result")
	}
}
func document() review.Document {
	return review.Document{Version: 3, BaseSha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StartSha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", HeadSha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ManifestSha256: "manifest", Graph: review.Graph{Nodes: []review.GraphNode{}, Edges: []review.GraphEdge{}, Limitations: []string{}, Sha256: "graph"}, Coverage: review.Coverage{IsComplete: true}, Limitations: []string{}, Blocks: []review.Block{{ID: "b1", OriginalBlockID: "b1", FilePath: "a.go", Kind: "file", Head: &review.CodeSlice{Path: "a.go", Revision: review.Head, StartLine: 1, EndLine: 1, Content: "package changed", SymbolStartLine: 1, SymbolEndLine: 1, IsComplete: true}, HeadExists: true, PartIndex: 1, PartCount: 1, SymbolIDs: []string{}, ChangedLines: []review.ChangedLine{{Revision: review.Head, Line: 1}}, Limitations: []string{}, StructuralContext: []review.CodeSlice{}}}}
}
func okModel(w http.ResponseWriter) {
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"checkedBlockIds":["b1"],"findings":[],"strengths":[],"unconfirmedDependencies":[]}`}, "finish_reason": "stop"}}})
}
func routeFor(t *testing.T, a, b string) (config.Config, config.Route) {
	t.Helper()
	c := config.Config{Version: 1, Routing: config.Routing{Primary: "primary"}, Profiles: map[string]config.Profile{"primary": {BaseURL: a, Model: "primary-model", ContextLength: 65536, MaximumAttempts: 1, TimeoutSeconds: 5}}}
	if b != "" {
		c.Routing.Fallback = "backup"
		c.Profiles["backup"] = config.Profile{BaseURL: b, Model: "backup-model", ContextLength: 32768, MaximumAttempts: 1, TimeoutSeconds: 5}
	}
	r, e := config.ResolveRouting(c, config.Overrides{})
	if e != nil {
		t.Fatal(e)
	}
	return c, r
}
func TestFallbackUsesItsOwnModelBudgetAndResumeDoesNotRepeat(t *testing.T) {
	var primary, backup atomic.Int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { primary.Add(1); w.WriteHeader(503) }))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backup.Add(1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "backup-model" {
			t.Error("wrong fallback model")
		}
		if body["max_tokens"].(float64) >= 32768 {
			t.Error("wrong fallback budget")
		}
		okModel(w)
	}))
	defer b.Close()
	c, r := routeFor(t, a.URL, b.URL)
	p, e := BuildDocument(document(), r, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "run")
	result, e := Run(context.Background(), p, c, dir)
	if e != nil {
		t.Fatal(e)
	}
	if result.Status != "complete" {
		t.Fatalf("status %s", result.Status)
	}
	if primary.Load() != 1 || backup.Load() != 1 {
		t.Fatalf("calls %d/%d", primary.Load(), backup.Load())
	}
	if _, e = Run(context.Background(), p, c, dir); e != nil {
		t.Fatal(e)
	}
	if primary.Load() != 1 || backup.Load() != 1 {
		t.Fatal("repeated completed inference")
	}
}
func TestAuthenticationDoesNotTriggerFallback(t *testing.T) {
	var fallback atomic.Int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallback.Add(1); okModel(w) }))
	defer b.Close()
	c, r := routeFor(t, a.URL, b.URL)
	p, e := BuildDocument(document(), r, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	result, e := Run(context.Background(), p, c, filepath.Join(t.TempDir(), "run"))
	if e == nil || result.Status == "complete" {
		t.Fatal("auth failure reported success")
	}
	if fallback.Load() != 0 {
		t.Fatal("auth triggered fallback")
	}
}

func TestFallbackIgnoresContextOfCompletedSibling(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int32
	respond := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var input struct {
			Batch struct {
				Blocks []struct {
					ID string `json:"id"`
				} `json:"blocks"`
			} `json:"batch"`
		}
		json.Unmarshal([]byte(req.Messages[1].Content), &input)
		ids := []string{}
		for _, b := range input.Batch.Blocks {
			ids = append(ids, b.ID)
		}
		model, _ := json.Marshal(map[string]any{"checkedBlockIds": ids, "findings": []any{}, "strengths": []any{}, "unconfirmedDependencies": []any{}})
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(model)}, "finish_reason": "stop"}}})
	}
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primaryCalls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		respond(w, r)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallbackCalls.Add(1); respond(w, r) }))
	defer b.Close()
	c, route := routeFor(t, a.URL, b.URL)
	for name, p := range c.Profiles {
		p.ContextLength = 32000
		p.MaximumOutputTokens = 1000
		c.Profiles[name] = p
	}
	route, _ = config.ResolveRouting(c, config.Overrides{})
	d := document()
	d.Blocks[0].Head.Content = strings.Repeat("a", 25000)
	sibling := d.Blocks[0]
	sibling.ID = "b2"
	sibling.OriginalBlockID = "b2"
	sibling.FilePath = "b.java"
	head := *sibling.Head
	head.Path = "b.java"
	head.StartLine = 3
	head.EndLine = 3
	head.SymbolStartLine = 3
	head.SymbolEndLine = 3
	head.Content = strings.Repeat("b", 25000)
	sibling.Head = &head
	sibling.ChangedLines = []review.ChangedLine{{Revision: review.Head, Line: 3}}
	sibling.StructuralContext = []review.CodeSlice{{Path: "b.java", Revision: review.Head, StartLine: 2, EndLine: 2, Content: "import changed.Type;", SymbolStartLine: 2, SymbolEndLine: 2, IsComplete: true}}
	d.Blocks = append(d.Blocks, sibling)
	d.Coverage.ContextLines = []review.ChangeDisposition{{Path: "b.java", Revision: review.Head, Line: 2, Reason: "context"}}
	p, e := BuildDocument(d, route, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Prepared.Batches) != 2 {
		t.Fatalf("expected two batches, got %d", len(p.Prepared.Batches))
	}
	result, e := Run(context.Background(), p, c, filepath.Join(t.TempDir(), "run"))
	if e != nil {
		t.Fatalf("%v: %v", e, result.Result.Limitations)
	}
	if fallbackCalls.Load() != 1 || primaryCalls.Load() != 2 {
		t.Fatalf("calls %d/%d", primaryCalls.Load(), fallbackCalls.Load())
	}
}
func TestInvalidModelOutputHasOneCorrection(t *testing.T) {
	var calls atomic.Int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"wrong":true}`}, "finish_reason": "stop"}}})
	}))
	defer a.Close()
	c, r := routeFor(t, a.URL, "")
	p, e := BuildDocument(document(), r, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "run")
	_, e = Run(context.Background(), p, c, dir)
	if e == nil {
		t.Fatal("invalid output accepted")
	}
	if calls.Load() != 2 {
		t.Fatalf("calls: %d", calls.Load())
	}
	_, _ = Run(context.Background(), p, c, dir)
	if calls.Load() != 2 {
		t.Fatal("resume repeated exhausted correction")
	}
}
