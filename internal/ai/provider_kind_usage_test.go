package ai

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func TestDatabaseRecordsSelectedProviderKind(t *testing.T) {
	for _, kind := range []config.ProviderKind{config.ProviderUnspecified, config.ProviderOpenAI, config.ProviderOpenRouter, config.ProviderXiaomiMiMo, config.ProviderDeepSeek, config.ProviderGroq, config.ProviderOllama, config.ProviderOpenAICompatible} {
		name := string(kind)
		if name == "" {
			name = "unspecified"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = db.Close() }()
				var saved sql.NullString
				if err := db.QueryRowContext(t.Context(), "SELECT provider_kind FROM api_requests WHERE request_id='selected-call'").Scan(&saved); err != nil || saved.String != string(kind) || saved.Valid != (kind != config.ProviderUnspecified) {
					t.Errorf("selected provider kind was not saved before sending: %+v %v", saved, err)
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			snapshot := provider(server.URL, false)
			snapshot.Profile.Provider = kind
			snapshot.Name = "arbitrary-profile-name"
			recorder := usage.NewRecorder(path)
			ctx := usage.WithRequestID(usage.WithRecorder(context.Background(), recorder), "selected-call")
			if _, err := Complete(ctx, snapshot, "", request()); err != nil {
				t.Fatal(err)
			}
			if err := recorder.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			var saved sql.NullString
			var display, profile, model, url string
			if err := db.QueryRowContext(t.Context(), "SELECT provider_kind,provider,profile,model,request_url FROM api_requests WHERE request_id='selected-call'").Scan(&saved, &display, &profile, &model, &url); err != nil {
				t.Fatal(err)
			}
			if saved.String != string(kind) || saved.Valid != (kind != config.ProviderUnspecified) || display != server.URL+"/v1" || profile != snapshot.Name || model != snapshot.Profile.Model || url != server.URL+"/v1/chat/completions" {
				t.Fatal("selected kind was inferred from endpoint, profile, model, or backend")
			}
		})
	}
}

func TestProbeRecordsSelectedProviderKind(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	snapshot := provider(server.URL, false)
	snapshot.Profile.Provider = config.ProviderXiaomiMiMo
	path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
	recorder := usage.NewRecorder(path)
	if _, err := Probe(usage.WithRecorder(context.Background(), recorder), snapshot, ""); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var kind, operation string
	if err := db.QueryRowContext(t.Context(), "SELECT provider_kind,operation FROM api_requests").Scan(&kind, &operation); err != nil || kind != "xiaomi-mimo" || operation != "connection-test" {
		t.Fatalf("probe lost its selected provider kind: %v", err)
	}
	report, err := usage.Generate(usage.Store{Path: path}, time.Now().UTC(), []int{7})
	if err != nil || report.Periods[0].Total.Requests != 1 {
		t.Fatal("selected provider kind broke usage reporting")
	}
}

func TestSelectedProviderKindIsIndependentOfKnownEndpoint(t *testing.T) {
	for _, kind := range []config.ProviderKind{config.ProviderUnspecified, config.ProviderDeepSeek, config.ProviderOpenAICompatible} {
		snapshot := config.ProviderSnapshot{Name: "arbitrary", Profile: config.Profile{Provider: kind, BaseURL: "https://api.openai.com/v1", Model: "custom", Backend: "openai-compatible"}}
		meta := usageMetadata(snapshot, "")
		if meta.ProviderKind != string(kind) || meta.Provider != "OpenAI" {
			t.Fatal("known endpoint overrode the explicitly selected kind")
		}
	}
}
