package tui

import (
	"encoding/json"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

func TestXiaomiMiMoSelectionSavesTypedProviderAndMarinaSettings(t *testing.T) {
	m := testModel(t)
	m.startEditor("")
	m.pickerKind = "preset"
	m.choose("Xiaomi MiMo")
	m.editor.fields[fieldName].SetValue("arbitrary-profile")
	m.editor.fields[fieldURL].SetValue("https://custom.example/v1")
	m.editor.fields[fieldModel].SetValue("custom-model-id")
	m.editor.auth = "none"
	if err := m.applyEditor(); err != nil {
		t.Fatal(err)
	}
	if !m.save() {
		t.Fatal(m.notice)
	}
	loaded, err := config.Load(m.path)
	p := loaded.Profiles["arbitrary-profile"]
	var thinking map[string]string
	if e := json.Unmarshal(p.ExtraParameters["thinking"], &thinking); e != nil {
		t.Fatal(e)
	}
	if err != nil || p.Provider != config.ProviderXiaomiMiMo || !p.Stream || p.OutputTokenParameter != "max_completion_tokens" || p.ResponseFormat != "json_object" || p.TimeoutSeconds != 3300 || thinking["type"] != "enabled" {
		t.Fatalf("saved MiMo preset is incomplete: %+v %v", p, err)
	}
	m.startEditor("arbitrary-profile")
	if m.editor.preset != "Xiaomi MiMo" {
		t.Fatal("custom URL or alias changed the provider selection")
	}
}

func TestProviderSelectionRemovesOnlyPreviousPresetDefaults(t *testing.T) {
	m := testModel(t)
	m.startEditor("")
	m.editor.original.ExtraParameters = map[string]json.RawMessage{"custom": json.RawMessage(`1`)}
	m.pickerKind = "preset"
	m.choose("Xiaomi MiMo")
	if _, exists := m.editor.original.ExtraParameters["thinking"]; !exists {
		t.Fatal("MiMo thinking default is missing")
	}
	m.pickerKind = "preset"
	m.choose("OpenAI")
	if _, exists := m.editor.original.ExtraParameters["thinking"]; exists {
		t.Fatal("MiMo default leaked into another provider")
	}
	if string(m.editor.original.ExtraParameters["custom"]) != `1` {
		t.Fatal("provider selection removed a user parameter")
	}
}
