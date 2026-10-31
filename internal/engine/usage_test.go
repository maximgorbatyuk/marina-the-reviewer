package engine

import (
	"context"
	"database/sql"
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
	profile.Provider = config.ProviderXiaomiMiMo
	cfg.Profiles["primary"] = profile
	fallback := cfg.Profiles["backup"]
	fallback.Provider = config.ProviderDeepSeek
	cfg.Profiles["backup"] = fallback
	route, err := config.ResolveRouting(cfg, config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	profile.Provider = config.ProviderOllama
	cfg.Profiles["primary"] = profile
	dir := t.TempDir()
	path := filepath.Join(dir, "stats", "stats.sqlite3")
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
	reviews := report.Periods[0].Reviews
	if reviews.Total != 1 || reviews.Complete != 1 || reviews.CheckedBlocks != 1 || reviews.AverageDurationMilliseconds == nil {
		t.Fatalf("resume changed logical review counts: %+v", reviews)
	}
	snapshot, err := (usage.Store{Path: path}).Snapshot(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range snapshot.Events {
		if event.RunID != plan.RunID || event.PassID != plan.PassID {
			t.Fatal("API attempt lost review identity")
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT profile,provider_kind FROM api_requests")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var profile, kind string
		if err := rows.Scan(&profile, &kind); err != nil {
			t.Fatal(err)
		}
		if profile == "primary" && kind != "xiaomi-mimo" || profile == "backup" && kind != "deepseek" {
			t.Fatal("request used the current config or primary kind instead of its saved provider")
		}
		counts[kind]++
	}
	if err := rows.Err(); err != nil || counts["xiaomi-mimo"] != 2 || counts["deepseek"] != 1 {
		t.Fatalf("retry, fallback or resume lost provider kinds: %+v %v", counts, err)
	}
}
