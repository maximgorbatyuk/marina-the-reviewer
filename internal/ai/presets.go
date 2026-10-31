package ai

import "github.com/maximgorbatyuk/marina-the-reviewer/internal/config"

type Preset struct {
	Name    string
	Profile config.Profile
	Local   bool
}

func Presets() []Preset {
	definitions := []struct {
		name, url, format string
		local             bool
	}{
		{"OpenAI", "https://api.openai.com/v1", "json_schema", false},
		{"OpenRouter", "https://openrouter.ai/api/v1", "json_schema", false},
		{"DeepSeek", "https://api.deepseek.com/chat/completions", "json_object", false},
		{"Groq", "https://api.groq.com/openai/v1", "json_schema", false},
		{"Ollama", "http://localhost:11434/v1", "json_schema", true},
		{"Other OpenAI-compatible", "", "json_schema", false},
	}
	result := make([]Preset, 0, len(definitions))
	for _, d := range definitions {
		result = append(result, Preset{Name: d.name, Local: d.local, Profile: config.Profile{
			Backend: "openai-compatible", BaseURL: d.url, ResponseFormat: d.format,
			OutputTokenParameter: "max_tokens", ContextLength: config.DefaultContextLength,
			MaximumConcurrentRequests: 1, TimeoutSeconds: 300, MaximumAttempts: 2,
		}})
	}
	return result
}
