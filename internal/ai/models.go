package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

func ListModels(ctx context.Context, provider config.ProviderSnapshot, token string) ([]string, error) {
	endpoint, err := chatEndpoint(provider.Profile.BaseURL)
	if err != nil {
		return nil, &Error{Code: "configuration", Message: "invalid provider URL"}
	}
	endpoint = strings.TrimSuffix(endpoint, "/chat/completions") + "/models"
	timeout := 20 * time.Second
	if seconds := provider.Profile.TimeoutSeconds; seconds > 0 && time.Duration(seconds)*time.Second < timeout {
		timeout = time.Duration(seconds) * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &Error{Code: "configuration", Message: "model request could not be created"}
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Accept", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		code := "network_error"
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
		return nil, &Error{Code: code, Message: "model request did not complete"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := "request_rejected"
		switch {
		case response.StatusCode == 401 || response.StatusCode == 403:
			code = "authentication"
		case response.StatusCode == 404 || response.StatusCode == 405 || response.StatusCode == 501:
			code = "models_unavailable"
		case response.StatusCode == 429:
			code = "rate_limit"
		case response.StatusCode >= 500:
			code = "server_error"
		case response.StatusCode >= 300 && response.StatusCode < 400:
			code = "configuration"
		}
		return nil, &Error{Code: code, Message: "model list unavailable; enter a model ID manually", Status: response.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Code: "network_error", Message: "model response could not be read"}
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if len(data) > 1<<20 || json.Unmarshal(data, &result) != nil {
		return nil, &Error{Code: "invalid_output", Message: "model list has an invalid format"}
	}
	models := []string{}
	for _, item := range result.Data {
		if item.ID != "" && len(item.ID) <= 256 && strings.IndexFunc(item.ID, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) == -1 {
			models = append(models, item.ID)
		}
	}
	slices.Sort(models)
	models = slices.Compact(models)
	if len(models) == 0 {
		return nil, &Error{Code: "models_unavailable", Message: "no models listed; enter a model ID manually"}
	}
	return models, nil
}
