package config

import (
	"encoding/json"
	"errors"
)

type ProviderKind string

const XiaomiMiMoBaseURL = "https://token-plan-sgp.xiaomimimo.com/v1"

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
