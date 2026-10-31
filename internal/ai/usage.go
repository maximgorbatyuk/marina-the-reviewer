package ai

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"unicode"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/redact"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func providerRoot(base string) string {
	endpoint, err := chatEndpoint(base)
	if err != nil {
		return "unknown"
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "unknown"
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/chat/completions")
	return u.String()
}

func usageMetadata(provider config.ProviderSnapshot, token string) usage.Metadata {
	root := providerRoot(provider.Profile.BaseURL)
	name := root
	for _, preset := range Presets() {
		if preset.Profile.BaseURL != "" && providerRoot(preset.Profile.BaseURL) == root {
			name = preset.Name
			break
		}
	}
	clean := func(value string) string {
		if token != "" {
			value = redact.Text(value, map[string]string{"provider": token})
		}
		value = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return -1
			}
			return r
		}, value)
		if value == "" {
			value = "unknown"
		}
		return value
	}
	return usage.Metadata{ProviderID: clean(root), Provider: clean(name), Profile: clean(provider.Name), Model: clean(provider.Profile.Model)}
}

func requestStreamUsage(profile config.Profile) bool {
	if !profile.Stream {
		return false
	}
	if _, explicit := profile.ExtraParameters["stream_options"]; explicit {
		return false
	}
	return providerRoot(profile.BaseURL) == "https://api.openai.com/v1"
}

func tokenCounts(value Usage) usage.Counts {
	convert := func(n *int) *int64 {
		if n == nil || *n < 0 {
			return nil
		}
		value := int64(*n)
		return &value
	}
	return usage.Counts{Input: convert(value.InputTokens), Output: convert(value.OutputTokens), Total: convert(value.TotalTokens)}
}

func usageStatus(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var providerErr *Error
	if errors.As(err, &providerErr) {
		return providerErr.Code
	}
	return "error"
}
