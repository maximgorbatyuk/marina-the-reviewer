package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

func TestMimoPresetSendsMarinaPayload(t *testing.T) {
	var preset *Preset
	for _, candidate := range Presets() {
		if candidate.Provider == config.ProviderXiaomiMiMo {
			cloned := candidate
			preset = &cloned
		}
		if candidate.Profile.TimeoutSeconds != 3300 {
			t.Errorf("preset %s has timeout %d", candidate.Name, candidate.Profile.TimeoutSeconds)
		}
	}
	if preset == nil {
		t.Fatal("the MiMo preset is missing")
	}
	preset.Profile.Model = "mimo-v2.6-pro"
	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)
	expected := map[string]any{
		"model": "mimo-v2.6-pro", "temperature": float64(0), "max_completion_tokens": float64(131072),
		"stream": true, "thinking": map[string]any{"type": "enabled"},
		"response_format": map[string]any{"type": "json_object"},
		"messages": []any{
			map[string]any{"role": "system", "content": "Review this MR.\n\nReturn JSON that matches this schema exactly:\n" + string(schema)},
			map[string]any{"role": "user", "content": "Pinned MR input"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(body, expected) {
			t.Errorf("payload differs from Marina: got %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"ok\\\":true}\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	preset.Profile.BaseURL = server.URL
	response, err := Complete(context.Background(), config.ProviderSnapshot{Name: "custom-mimo", Profile: preset.Profile}, "", Request{
		SystemPrompt: "Review this MR.", UserPrompt: "Pinned MR input", Schema: schema, MaximumOutputTokens: 131072,
	})
	if err != nil || response.FinishReason != "stop" {
		t.Fatalf("MiMo completion failed: %+v %v", response, err)
	}
}
