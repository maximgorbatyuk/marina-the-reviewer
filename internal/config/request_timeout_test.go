package config

import "testing"

func TestRequestTimeoutDefaultsAndClientOverride(t *testing.T) {
	cfg := Config{Version: 1, Routing: Routing{Primary: "primary", Fallback: "backup"}, Profiles: map[string]Profile{
		"primary": {BaseURL: "https://primary.example/v1", Model: "mimo-v2.6-pro"},
		"backup":  {BaseURL: "https://backup.example/v1", Model: "deepseek-v4-pro", TimeoutSeconds: 1200},
	}}
	route, err := ResolveRouting(cfg, Overrides{})
	if err != nil || route.Primary.Profile.TimeoutSeconds != 3300 || route.Fallback.Profile.TimeoutSeconds != 1200 {
		t.Fatalf("wrong default or explicit profile timeout: %+v %v", route, err)
	}
	route, err = ResolveRouting(cfg, Overrides{RequestTimeoutSeconds: 2400})
	if err != nil || route.Primary.Profile.TimeoutSeconds != 2400 || route.Fallback.Profile.TimeoutSeconds != 2400 {
		t.Fatalf("client timeout did not apply to both providers: %+v %v", route, err)
	}
	if cfg.Profiles["primary"].TimeoutSeconds != 0 || cfg.Profiles["backup"].TimeoutSeconds != 1200 {
		t.Fatal("request timeout changed the saved configuration")
	}
	for _, invalid := range []int{-1, 86401} {
		if _, err := ResolveRouting(cfg, Overrides{RequestTimeoutSeconds: invalid}); err == nil {
			t.Fatalf("accepted invalid client timeout %d", invalid)
		}
	}
}
