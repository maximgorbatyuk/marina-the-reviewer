package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPresetsUseExistingBackend(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets() {
		if p.Name == "" || seen[p.Name] {
			t.Fatal("duplicate or empty preset")
		}
		seen[p.Name] = true
		if p.Profile.Backend != "openai-compatible" || p.Profile.Model != "" {
			t.Fatal("preset changed backend or selected a model")
		}
	}
	if len(seen) < 6 {
		t.Fatal("provider list incomplete")
	}
}

func TestListModelsPathsAndAuthentication(t *testing.T) {
	for _, path := range []string{"/v1", "/api/v1", "/openai/v1", "/api/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := strings.TrimSuffix(path, "/chat/completions") + "/models"
				if r.URL.Path != want || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-value" {
					t.Error("wrong model discovery request")
				}
				fmt.Fprint(w, `{"data":[{"id":"z"},{"id":"a"},{"id":"a"},{"id":"bad\u001b[2J"}]}`)
			}))
			defer s.Close()
			p := provider(s.URL, false)
			p.Profile.BaseURL = s.URL + path
			models, err := ListModels(context.Background(), p, "test-value")
			if err != nil || !reflect.DeepEqual(models, []string{"a", "z"}) {
				t.Fatalf("models %v: %v", models, err)
			}
		})
	}
}

func TestListModelsErrorsAndCancellation(t *testing.T) {
	for status, code := range map[int]string{401: "authentication", 403: "authentication", 404: "models_unavailable", 405: "models_unavailable", 429: "rate_limit", 500: "server_error", 501: "models_unavailable", 302: "configuration"} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "reflected-test-value")
			}))
			defer s.Close()
			_, err := ListModels(context.Background(), provider(s.URL, false), "reflected-test-value")
			var e *Error
			if !errors.As(err, &e) || e.Code != code || strings.Contains(err.Error(), "reflected-test-value") {
				t.Fatalf("wrong safe error: %v", err)
			}
		})
	}
	for _, body := range []string{`broken`, `{"data":null}`, `{"data":[]}`, `{"data":[{"id":"a"}]} trailing`, strings.Repeat("x", (1<<20)+1)} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := ListModels(context.Background(), provider(s.URL, false), "")
		s.Close()
		if err == nil {
			t.Fatal("unavailable or malformed models accepted")
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := ListModels(ctx, provider(s.URL, false), ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestProbeUsesSelectedProfileOnce(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-value" || string(body["model"]) != `"model"` || string(body["max_completion_tokens"]) != "128" || string(body["reasoning_effort"]) != `"low"` {
			t.Error("profile settings ignored")
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]}`)
	}))
	defer s.Close()
	p := provider(s.URL, false)
	p.Profile.MaximumOutputTokens = 128
	p.Profile.ExtraParameters = map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"low"`)}
	result, err := Probe(context.Background(), p, "test-value")
	if err != nil || result.Elapsed <= 0 || calls.Load() != 1 {
		t.Fatalf("probe: %v, calls %d", err, calls.Load())
	}
}

func TestProbeFailureHasNoRetryOrSensitiveOutput(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
		fmt.Fprint(w, "test-value")
	}))
	defer s.Close()
	p := provider(s.URL, false)
	p.Profile.MaximumAttempts = 5
	_, err := Probe(context.Background(), p, "test-value")
	if err == nil || strings.Contains(err.Error(), "test-value") || calls.Load() != 1 {
		t.Fatal("probe retried or exposed server content")
	}
}

func TestListModelsDoesNotFollowRedirect(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	if _, err := ListModels(context.Background(), provider(source.URL, false), "synthetic-private-value"); err == nil || leaked.Load() != 0 {
		t.Fatal("redirect forwarded request")
	}
}
