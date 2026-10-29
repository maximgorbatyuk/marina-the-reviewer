package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

var schema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["findings"],"properties":{"findings":{"type":"array"}}}`)

func provider(url string, stream bool) config.ProviderSnapshot {
	return config.ProviderSnapshot{Name: "test", Profile: config.Profile{BaseURL: url + "/v1", Model: "model", ContextLength: 16000, TimeoutSeconds: 5, ResponseFormat: "json_object", OutputTokenParameter: "max_completion_tokens", Stream: stream}}
}
func request() Request {
	return Request{SystemPrompt: "review", UserPrompt: "code", Schema: schema, MaximumOutputTokens: 1000}
}
func TestJSONModePayloadAndUsage(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("wrong request")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body["max_completion_tokens"]) != "1000" {
			t.Error("wrong output limit")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}}`))
	}))
	defer s.Close()
	result, err := Complete(context.Background(), provider(s.URL, false), "token", request())
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != "stop" || result.Usage.TotalTokens == nil || *result.Usage.TotalTokens != 16 {
		t.Fatalf("result %+v", result)
	}
}
func TestSSEReassemblesOutputAndRejectsMissingFinish(t *testing.T) {
	for _, finished := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "incomplete"}[finished], func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"findings\\\":[] }\"}}]}\n\n"))
				if finished {
					w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
				}
			}))
			defer s.Close()
			_, err := Complete(context.Background(), provider(s.URL, true), "", request())
			if (err == nil) != finished {
				t.Fatalf("finished %v: %v", finished, err)
			}
		})
	}
}
func TestStatusErrorsAreSafeAndClassified(t *testing.T) {
	for status, want := range map[int]string{401: "authentication", 429: "rate_limit", 503: "server_error"} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte("do not leak token"))
		}))
		_, err := Complete(context.Background(), provider(s.URL, false), "token", request())
		s.Close()
		e, ok := err.(*Error)
		if !ok || e.Code != want {
			t.Fatalf("status %d: %v", status, err)
		}
		if e.Message == "do not leak token" {
			t.Fatal("leaked body")
		}
	}
}
func TestSchemaInvalidOutputIsNotTransportSuccess(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"unknown\":true}"},"finish_reason":"stop"}]}`))
	}))
	defer s.Close()
	_, err := Complete(context.Background(), provider(s.URL, false), "", request())
	e, ok := err.(*Error)
	if !ok || e.Code != "invalid_output" {
		t.Fatalf("error: %v", err)
	}
}
