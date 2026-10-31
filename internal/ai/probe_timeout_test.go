package ai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type deadlineProbeTransport struct{ testing *testing.T }

func (transport deadlineProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	deadline, ok := request.Context().Deadline()
	if !ok || time.Until(deadline) < 3290*time.Second {
		transport.testing.Error("connection test shortened the configured AI request timeout")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]}`))}, nil
}

func TestConnectionProbeUsesTheConfiguredRequestTimeout(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = deadlineProbeTransport{testing: t}
	t.Cleanup(func() { http.DefaultTransport = original })
	p := provider("https://provider.example/v1", false)
	p.Profile.TimeoutSeconds = 3300
	if _, err := Probe(context.Background(), p, ""); err != nil {
		t.Fatal(err)
	}
}
