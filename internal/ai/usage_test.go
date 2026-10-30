package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func TestUsageTracksInvalidOutputAndNoSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
	r := usage.NewRecorder(path)
	ctx := usage.WithRecorder(context.Background(), r)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"model-output-secret"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}}`)
	}))
	defer server.Close()
	_, err := Complete(ctx, provider(server.URL, false), "synthetic-private-value", Request{SystemPrompt: "private-prompt", UserPrompt: "private-source-code", Schema: schema, MaximumOutputTokens: 1000})
	if err == nil {
		t.Fatal("invalid output accepted")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := usage.Generate(usage.Store{Path: path}, time.Now().UTC(), []int{7})
	if err != nil {
		t.Fatal(err)
	}
	total := report.Periods[0].Total
	if total.Requests != 1 || total.FailedRequests != 1 || total.TotalTokens == nil || *total.TotalTokens != 16 {
		t.Fatal("consumed tokens on invalid output lost")
	}
	data, _ := os.ReadFile(path)
	for _, secret := range []string{"synthetic-private-value", "private-prompt", "private-source-code", "model-output-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("sensitive data in usage log")
		}
	}
}

func TestUsagePreservesCountsFromInvalidEnvelopes(t *testing.T) {
	for _, body := range []string{
		`{"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}`,
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"total_tokens\":11}}\n\ndata: [DONE]\n\n",
		"data: {\"error\":{\"message\":\"failed\"},\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"total_tokens\":11}}\n\n",
	} {
		path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
		r := usage.NewRecorder(path)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := Complete(usage.WithRecorder(context.Background(), r), provider(server.URL, false), "", request())
		server.Close()
		if err == nil {
			t.Fatal("invalid envelope accepted")
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		report, err := usage.Generate(usage.Store{Path: path}, time.Now().UTC(), []int{7})
		if err != nil || len(report.Periods[0].Providers) != 1 || report.Periods[0].Total.TotalTokens == nil || *report.Periods[0].Total.TotalTokens != 11 {
			t.Fatal("provider usage lost with envelope error")
		}
	}
}

func TestProviderIdentityNormalizesEndpoints(t *testing.T) {
	for _, url := range []string{"https://api.openai.com", "https://api.openai.com/v1/", "https://api.openai.com/v1/chat/completions"} {
		meta := usageMetadata(config.ProviderSnapshot{Name: "cloud", Profile: config.Profile{BaseURL: url, Model: "chosen"}}, "")
		if meta.Provider != "OpenAI" || meta.ProviderID != "https://api.openai.com/v1" {
			t.Fatalf("wrong provider %s", meta.Provider)
		}
	}
}

func TestStreamingUsageOptionsPreserveExplicitSettings(t *testing.T) {
	p := provider("https://api.openai.com", true)
	if !requestStreamUsage(p.Profile) {
		t.Fatal("known streaming API usage not requested")
	}
	p.Profile.ExtraParameters = map[string]json.RawMessage{"stream_options": json.RawMessage(`{"include_usage":false}`)}
	if requestStreamUsage(p.Profile) {
		t.Fatal("explicit stream options replaced")
	}
	p = provider("https://custom.example", true)
	if requestStreamUsage(p.Profile) {
		t.Fatal("unknown API request changed implicitly")
	}
}

func TestInterruptedSSEKeepsReceivedUsageAfterComments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		conn, writer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		payload := ": keepalive\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"total_tokens\":11}}\n\n"
		fmt.Fprintf(writer, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(payload), payload)
		writer.Flush()
	}))
	defer server.Close()
	response, err := Complete(context.Background(), provider(server.URL, true), "", request())
	if err == nil || response.Usage.TotalTokens == nil || *response.Usage.TotalTokens != 11 {
		t.Fatal("received usage lost on interrupted SSE")
	}
}

func TestGroqNestedStreamingUsage(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"{}\"},\"finish_reason\":\"stop\"}],\"x_groq\":{\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}}\n\ndata: [DONE]\n\n")
	_, _, _, counts, err := parseSSE(body)
	if err != nil || counts.TotalTokens == nil || *counts.TotalTokens != 6 {
		t.Fatal("Groq usage not decoded")
	}
}
