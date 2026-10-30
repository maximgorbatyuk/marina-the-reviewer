package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func TestUsageCountsRetriesFallbackAndNotResume(t *testing.T) {
	var primary, backup atomic.Int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { primary.Add(1); w.WriteHeader(503) }))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backup.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"checkedBlockIds":["b1"],"findings":[],"strengths":[],"unconfirmedDependencies":[]}`}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 14, "completion_tokens": 6, "total_tokens": 20}})
	}))
	defer b.Close()
	cfg, _ := routeFor(t, a.URL, b.URL)
	profile := cfg.Profiles["primary"]
	profile.MaximumAttempts = 2
	cfg.Profiles["primary"] = profile
	route, err := config.ResolveRouting(cfg, config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	runDir := filepath.Join(dir, "run")
	for i := 0; i < 2; i++ {
		recorder := usage.NewRecorder(path)
		ctx := usage.WithOperation(usage.WithRecorder(context.Background(), recorder), "review")
		result, err := Run(ctx, plan, cfg, runDir)
		if err != nil || result.Status != "complete" {
			t.Fatalf("run %d: %v", i, err)
		}
		if err := recorder.Close(); err != nil {
			t.Fatal(err)
		}
	}
	report, err := usage.Generate(usage.Store{Path: path}, time.Now().UTC(), []int{7})
	if err != nil {
		t.Fatal(err)
	}
	total := report.Periods[0].Total
	if primary.Load() != 2 || backup.Load() != 1 || total.Requests != 3 || len(report.Periods[0].Providers) != 2 || total.UnknownUsageRequests != 2 || total.TotalTokens == nil || *total.TotalTokens != 20 {
		t.Fatalf("bad physical request counts: %+v", total)
	}
}
