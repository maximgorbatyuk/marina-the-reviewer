package ai

import (
	"encoding/json"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

// Preset keeps provider defaults together so setup does not need network discovery.
type Preset struct {
	Provider config.ProviderKind
	Name     string
	Profile  config.Profile
	Local    bool
}

// Presets returns independent copies of the supported provider defaults.
func Presets() []Preset {
	definitions := []struct {
		provider          config.ProviderKind
		name, url, format string
		local             bool
	}{
		{config.ProviderOpenAI, "OpenAI", "https://api.openai.com/v1", "json_schema", false},
		{config.ProviderOpenRouter, "OpenRouter", "https://openrouter.ai/api/v1", "json_schema", false},
		{config.ProviderDeepSeek, "DeepSeek", "https://api.deepseek.com/chat/completions", "json_object", false},
		{config.ProviderGroq, "Groq", "https://api.groq.com/openai/v1", "json_schema", false},
		{config.ProviderOllama, "Ollama", "http://localhost:11434/v1", "json_schema", true},
		{config.ProviderXiaomiMiMo, "Xiaomi MiMo", config.XiaomiMiMoBaseURL, "json_object", false},
		{config.ProviderOpenAICompatible, "Other OpenAI-compatible", "", "json_schema", false},
	}
	result := make([]Preset, 0, len(definitions))
	for _, d := range definitions {
		result = append(result, Preset{Provider: d.provider, Name: d.name, Local: d.local, Profile: config.Profile{
			Provider: d.provider,
			Backend:  "openai-compatible", BaseURL: d.url, ResponseFormat: d.format,
			OutputTokenParameter: "max_tokens", ContextLength: config.DefaultContextLength,
			MaximumConcurrentRequests: 1, TimeoutSeconds: config.DefaultRequestTimeoutSeconds, MaximumAttempts: 2,
		}})
		if d.provider == config.ProviderXiaomiMiMo {
			profile := &result[len(result)-1].Profile
			profile.OutputTokenParameter = "max_completion_tokens"
			profile.Stream = true
			profile.ExtraParameters = map[string]json.RawMessage{"thinking": json.RawMessage(`{"type":"enabled"}`)}
		}
	}
	return result
}
