package ai

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

// ProbeResult reports the outcome of a small setup request before review execution.
type ProbeResult struct {
	Elapsed time.Duration
	Usage   Usage
}

// Probe checks inference access with a small request that contains no source code.
func Probe(ctx context.Context, provider config.ProviderSnapshot, token string) (ProbeResult, error) {
	started := time.Now()
	ctx = usage.WithOperation(ctx, "connection-test")
	budget := 1024
	if n := provider.Profile.ContextLength; n > 0 {
		budget = min(budget, n/4)
	}
	if n := provider.Profile.MaximumOutputTokens; n > 0 {
		budget = min(budget, n)
	}
	response, err := Complete(ctx, provider, token, Request{
		SystemPrompt:        "Return only a JSON object with ok set to true.",
		UserPrompt:          "Check the API connection. Return {\"ok\":true}.",
		Schema:              json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean","const":true}},"required":["ok"],"additionalProperties":false}`),
		MaximumOutputTokens: budget,
	})
	result := ProbeResult{Elapsed: time.Since(started), Usage: response.Usage}
	var providerErr *Error
	if errors.As(err, &providerErr) {
		return result, &Error{Code: providerErr.Code, Message: providerErr.Message, Status: providerErr.Status}
	}
	return result, err
}
