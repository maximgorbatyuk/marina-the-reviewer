package ai

import "testing"

func TestNewProviderPresetsUseOneMillionTokenContext(t *testing.T) {
	for _, preset := range Presets() {
		if preset.Profile.ContextLength != 1048576 {
			t.Fatalf("preset %s has context %d", preset.Name, preset.Profile.ContextLength)
		}
	}
}
