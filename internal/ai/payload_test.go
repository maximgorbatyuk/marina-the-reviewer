package ai

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func TestDatabaseKeepsProviderExchange(t *testing.T) {
	for _, scenario := range []string{"json", "sse", "invalid", "http-error", "interrupted", "no-headers", "large-response", "large-sse", "large-request", "binary-error", "redacted", "all-provider-keys"} {
		t.Run(scenario, func(t *testing.T) {
			body := []byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"},"finish_reason":"stop"}]}`)
			status := http.StatusOK
			user := "source for analysis"
			token := "synthetic-private-token"
			switch scenario {
			case "sse":
				body = []byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"findings\\\":[]}\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			case "invalid":
				body = []byte(`{"broken":`)
			case "http-error":
				status, body = http.StatusServiceUnavailable, []byte(strings.Repeat("x", 65537)+"error-tail")
			case "interrupted":
				body = []byte("partial response")
			case "large-response":
				body = []byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"},"finish_reason":"stop"}],"padding":"` + strings.Repeat("x", 17<<20) + `response-tail"}`)
			case "large-sse":
				body = []byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"findings\\\":[]}\"},\"finish_reason\":\"stop\"}],\"padding\":\"" + strings.Repeat("x", 17<<20) + "\"}\n\ndata: [DONE]\n\n")
			case "large-request":
				user = strings.Repeat("source", 3<<20) + "request-tail"
			case "binary-error":
				status, body = http.StatusBadGateway, []byte{0xff, 0x00, 0xfe, 'x'}
			case "redacted":
				user = `source with \u0073ynthetic-private-token`
				status, body = http.StatusUnauthorized, []byte(`{"error":"\u0073ynthetic-private-token denied"}`)
			case "all-provider-keys":
				user = "source with secondary-private-token"
				status, body = http.StatusUnauthorized, []byte(`{"error":"secondary-private-token denied"}`)
			}
			received := make(chan []byte, 1)
			path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
			recorder := usage.NewRecorder(path)
			ctx := usage.WithRequestID(usage.WithRecorder(context.Background(), recorder), "physical-attempt")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				requestBytes, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				received <- requestBytes
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = db.Close() }()
				var saved []byte
				var complete sql.NullBool
				var selectedKind string
				if err := db.QueryRowContext(t.Context(), "SELECT request_payload,response_complete,provider_kind FROM api_requests WHERE request_id='physical-attempt'").Scan(&saved, &complete, &selectedKind); err != nil {
					t.Error("request was not saved before sending:", err)
				} else if selectedKind != "groq" || complete.Valid || scenario != "redacted" && scenario != "all-provider-keys" && !bytes.Equal(saved, requestBytes) {
					t.Error("started request changed its payload or claimed a response")
				}
				if scenario == "no-headers" {
					<-r.Context().Done()
					return
				}
				if scenario == "sse" || scenario == "large-sse" {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				if scenario == "interrupted" {
					w.Header().Set("Content-Length", fmt.Sprint(len(body)+10))
				}
				w.WriteHeader(status)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			input := request()
			input.UserPrompt = user
			if scenario == "all-provider-keys" {
				input.RedactionSecrets = map[string]string{"current-provider": "secondary-private-token"}
			}
			snapshot := provider(server.URL, scenario == "sse" || scenario == "large-sse")
			snapshot.Profile.Provider = config.ProviderGroq
			if scenario == "no-headers" {
				snapshot.Profile.TimeoutSeconds = 1
			}
			result, callErr := Complete(ctx, snapshot, token, input)
			if scenario == "json" || scenario == "sse" || scenario == "large-response" || scenario == "large-sse" || scenario == "large-request" {
				if callErr != nil {
					t.Fatal(callErr)
				}
			} else if callErr == nil {
				t.Fatal("expected provider failure")
			}
			if err := recorder.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			var requestBody, responseBody []byte
			var url, profile, model, providerName, selectedKind string
			var httpStatus int
			var complete sql.NullBool
			if err := db.QueryRowContext(t.Context(), "SELECT request_url,request_payload,response_payload,http_status,response_complete,profile,model,provider,provider_kind FROM api_requests WHERE request_id='physical-attempt'").Scan(&url, &requestBody, &responseBody, &httpStatus, &complete, &profile, &model, &providerName, &selectedKind); err != nil {
				t.Fatal(err)
			}
			requestBytes := <-received
			if selectedKind != "groq" || url != server.URL+"/v1/chat/completions" || profile != "test" || model != "model" || providerName == "" {
				t.Fatal("provider identity or response state was lost")
			}
			if scenario == "no-headers" {
				if httpStatus != 0 || complete.Valid || responseBody != nil || !bytes.Equal(requestBody, requestBytes) {
					t.Fatal("response was invented before receiving headers")
				}
			} else if httpStatus != status || !complete.Valid || complete.Bool != (scenario != "interrupted") {
				t.Fatal("HTTP response state was lost")
			} else if scenario == "redacted" || scenario == "all-provider-keys" {
				for _, value := range [][]byte{requestBody, responseBody} {
					if bytes.Contains(value, []byte("ynthetic-private-token")) || bytes.Contains(value, []byte("secondary-private-token")) || !bytes.Contains(value, []byte("[REDACTED]")) {
						t.Fatal("escaped provider key was not redacted")
					}
				}
			} else if !bytes.Equal(requestBody, requestBytes) || !bytes.Equal(responseBody, body) || result.ResponsePayload != string(body) {
				t.Fatal("request or response bytes were changed or truncated")
			}
			report, err := usage.Generate(usage.Store{Path: path}, time.Now().UTC(), []int{7})
			encoded, _ := json.Marshal(report)
			if err != nil || report.Periods[0].Total.Requests != 1 || bytes.Contains(encoded, []byte("request_payload")) || bytes.Contains(encoded, []byte(user)) {
				t.Fatal("payload changed or leaked into usage reporting")
			}
		})
	}
}
