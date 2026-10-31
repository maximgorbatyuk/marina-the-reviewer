package cli

import (
	"bytes"
	"io"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

func TestRequestTimeoutFlagControlsNewRoute(t *testing.T) {
	t.Setenv("MARINADOREVIEW_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, err := parseArguments("review", []string{"--base-url", "https://provider.example/v1", "--model", "review-model", "--no-fallback", "--request-timeout-seconds", "2400"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_, route, err := routing(a)
	if err != nil || route.Primary.Profile.TimeoutSeconds != 2400 {
		t.Fatalf("client timeout was not frozen: %+v %v", route, err)
	}
}

func TestSavedPlanRejectsRequestTimeoutOverride(t *testing.T) {
	var out, diagnostic bytes.Buffer
	code := Run([]string{"run", "--input", "-", "--request-timeout-seconds", "3300"}, bytes.NewBufferString(`{}`), &out, &diagnostic)
	if code != 2 || out.Len() != 0 {
		t.Fatalf("saved plan accepted a changed timeout: exit %d, output %s, diagnostic %s", code, out.String(), diagnostic.String())
	}
}

func TestDirectMimoRouteUsesMarinaWireSettings(t *testing.T) {
	t.Setenv("MARINADOREVIEW_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, route, err := routing(arguments{provider: "xiaomi-mimo", baseURL: "https://mimo.example/v1", model: "custom-model-id", noFallback: true})
	p := route.Primary.Profile
	if err != nil || !p.Stream || p.ResponseFormat != "json_object" || p.OutputTokenParameter != "max_completion_tokens" || p.TimeoutSeconds != 3300 || string(p.ExtraParameters["thinking"]) != `{"type":"enabled"}` {
		t.Fatalf("direct MiMo settings differ from Marina: %+v %v", route, err)
	}
	_, other, err := routing(arguments{provider: "openai", baseURL: "https://other.example/v1", model: "mimo-v2.6-pro", noFallback: true})
	if err != nil || other.Primary.Profile.Provider != config.ProviderOpenAI || other.Primary.Profile.Stream || other.Primary.Profile.ResponseFormat != "json_schema" {
		t.Fatalf("model name changed the explicit provider: %+v %v", other, err)
	}
}

func TestDirectXiaomiMiMoCanUseItsStandardURL(t *testing.T) {
	t.Setenv("MARINADOREVIEW_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, route, err := routing(arguments{provider: "xiaomi-mimo", model: "chosen-model", noFallback: true})
	if err != nil || route.Primary.Profile.BaseURL != "https://token-plan-sgp.xiaomimimo.com/v1" || route.Primary.Profile.Provider != config.ProviderXiaomiMiMo {
		t.Fatalf("direct MiMo default URL was not selected: %+v %v", route, err)
	}
}
