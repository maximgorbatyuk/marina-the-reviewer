package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

var schema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["findings"],"properties":{"findings":{"type":"array"}}}`)

func provider(url string, stream bool) config.ProviderSnapshot {
	return config.ProviderSnapshot{Name: "test", Profile: config.Profile{BaseURL: url + "/v1", Model: "model", ContextLength: 16000, TimeoutSeconds: 5, ResponseFormat: "json_object", OutputTokenParameter: "max_completion_tokens", Stream: stream}}
}

func TestHTTPErrorResponsePayloadTruncation(t *testing.T) {
	for status, code := range map[int]string{401: "authentication", 429: "rate_limit", 503: "server_error"} {
		for _, tc := range []struct {
			name, body string
			incomplete bool
			truncated  bool
		}{
			{name: "complete", body: "complete body"},
			{name: "unexpected-eof", body: "partial body", incomplete: true, truncated: true},
			{name: "size-limit", body: strings.Repeat("x", 65537), truncated: true},
		} {
			t.Run(strconv.Itoa(status)+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if tc.incomplete {
						w.Header().Set("Content-Length", strconv.Itoa(len(tc.body)+1))
					}
					w.WriteHeader(status)
					w.Write([]byte(tc.body))
				}))
				defer server.Close()
				result, err := Complete(context.Background(), provider(server.URL, false), "", request())
				var failure *Error
				if !errors.As(err, &failure) || failure.Code != code || failure.Status != status {
					t.Fatalf("wrong HTTP error: %v", err)
				}
				if result.ResponsePayloadTruncated != tc.truncated || result.ResponsePayload != tc.body[:min(len(tc.body), 65536)] || result.RawResponse != result.ResponsePayload {
					t.Fatal("HTTP error payload has the wrong content or truncation flag")
				}
			})
		}
	}
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

func TestMalformedEnvelopeAndInterruptedStreamKeepDiagnosticText(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "envelope", true: "stream"}[streaming], func(t *testing.T) {
			body := `{"broken":`
			if streaming {
				body = "data: {\"choices\":[{\"delta\":{\"content\":\"partial-text\"}}]}\n\n"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				w.Write([]byte(body))
			}))
			defer server.Close()
			result, err := Complete(context.Background(), provider(server.URL, streaming), "", request())
			if err == nil || result.ResponsePayload != body || result.RawResponse != body || streaming && result.RawOutput != "partial-text" {
				t.Fatalf("lost diagnostic: %+v %v", result, err)
			}
		})
	}
}

func TestRequestDeadlineDuringResponseBodyIsTimeout(t *testing.T) {
	streamed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"private-reasoning\",\"content\":\"partial-output\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		close(streamed)
		<-r.Context().Done()
	}))
	defer server.Close()
	snapshot := provider(server.URL, true)
	snapshot.Profile.TimeoutSeconds = 1
	result, err := Complete(context.Background(), snapshot, "", request())
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != "timeout" || !IsRetryable(err) {
		t.Fatalf("wrong body deadline error: %v", err)
	}
	select {
	case <-streamed:
	default:
		t.Fatal("request timed out before its response body started")
	}
	if result.RawOutput != "partial-output" || result.ResponsePayload == "" || !result.ResponsePayloadTruncated {
		t.Fatal("timeout lost its partial response diagnostic")
	}
	for _, detail := range []string{"timeout 1s", "received ", "content 14 bytes", "reasoning 17 bytes", "terminal finish reason absent"} {
		if !strings.Contains(failure.Message, detail) {
			t.Fatalf("timeout has no %q diagnostic: %s", detail, failure.Message)
		}
	}
	if strings.Contains(failure.Message, "partial-output") || strings.Contains(failure.Message, "private-reasoning") {
		t.Fatal("timeout diagnostic contains provider text")
	}
	if result.Elapsed <= 0 {
		t.Fatal("timeout has no elapsed duration")
	}
}

func TestRequestDeadlineBeforeHeadersReportsResponseWasNotStarted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		io.Copy(io.Discard, request.Body)
		<-request.Context().Done()
	}))
	defer server.Close()
	snapshot := provider(server.URL, false)
	snapshot.Profile.TimeoutSeconds = 1
	response, err := Complete(context.Background(), snapshot, "", request())
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != "timeout" || !strings.Contains(failure.Message, "no response headers") || !strings.Contains(failure.Message, "timeout 1s") {
		t.Fatalf("response-start state was lost: %v", err)
	}
	if response.ResponsePayload != "" || response.Elapsed <= 0 {
		t.Fatal("a response body was reported before headers arrived")
	}
}
