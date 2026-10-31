package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

func TestResponseSchemaDoesNotDependOnWorkingDirectory(t *testing.T) {
	for _, directory := range []string{"Application Support", "Марина", "Application Support/Марина #1"} {
		t.Run(directory, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), directory)
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(path)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				output := `{"checkedBlockIds":[],"findings":[],"strengths":[],"unconfirmedDependencies":[]}`
				if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
					"message": map[string]string{"content": output}, "finish_reason": "stop",
				}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			provider := config.ProviderSnapshot{Name: "fixture", Profile: config.Profile{
				BaseURL: server.URL, Model: "fixture", ResponseFormat: "json_object", TimeoutSeconds: 5,
			}}
			response, err := Complete(context.Background(), provider, "fixture-api-token", Request{
				Schema: review.ResponseSchema(), MaximumOutputTokens: 1024,
			})
			if err != nil || response.FinishReason != "stop" || calls.Load() != 1 {
				t.Fatalf("schema failed in %q: calls=%d response=%+v error=%v", path, calls.Load(), response, err)
			}
		})
	}
}

func TestSchemaCompileDiagnosticRetainsCauseAndRedactsCredentials(t *testing.T) {
	const token = "fixture-sensitive-schema-token"
	_, err := Complete(context.Background(), config.ProviderSnapshot{}, token, Request{
		Schema: json.RawMessage(`{"type":"` + token + `"}`), MaximumOutputTokens: 1024,
	})
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != "configuration" {
		t.Fatalf("invalid schema did not return a configuration error: %v", err)
	}
	if !strings.Contains(failure.Message, "/type") || strings.Contains(failure.Message, token) {
		t.Fatalf("schema diagnostic lost its cause or exposed a credential: %s", failure.Message)
	}
}
