package config

import (
	"encoding/json"
	"errors"
)

// ProviderKind preserves the selected provider identity independently of endpoint compatibility.
type ProviderKind string

// XiaomiMiMoBaseURL provides the preset endpoint without inferring identity from an arbitrary URL.
const XiaomiMiMoBaseURL = "https://token-plan-sgp.xiaomimimo.com/v1"

// ProviderUnspecified keeps older configurations valid when they have no selected provider kind.
const (
	ProviderUnspecified      ProviderKind = ""
	ProviderOpenAI           ProviderKind = "openai"
	ProviderOpenRouter       ProviderKind = "openrouter"
	ProviderXiaomiMiMo       ProviderKind = "xiaomi-mimo"
	ProviderDeepSeek         ProviderKind = "deepseek"
	ProviderGroq             ProviderKind = "groq"
	ProviderOllama           ProviderKind = "ollama"
	ProviderOpenAICompatible ProviderKind = "openai-compatible"
)

// ParseProviderKind rejects unknown provider identities before they enter stored configuration.
func ParseProviderKind(value string) (ProviderKind, error) {
	kind := ProviderKind(value)
	switch kind {
	case ProviderUnspecified, ProviderOpenAI, ProviderOpenRouter, ProviderXiaomiMiMo,
		ProviderDeepSeek, ProviderGroq, ProviderOllama, ProviderOpenAICompatible:
		return kind, nil
	default:
		return ProviderUnspecified, errors.New("unsupported provider identity")
	}
}

// UnmarshalJSON validates provider identity when configuration is decoded.
func (kind *ProviderKind) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return errors.New("provider identity must be a string")
	}
	var value *string
	if err := json.Unmarshal(data, &value); err != nil || value == nil {
		return errors.New("provider identity must be a string")
	}
	parsed, err := ParseProviderKind(*value)
	if err != nil {
		return err
	}
	*kind = parsed
	return nil
}
