package config

import (
	"encoding/json"
	"testing"
)

func TestProviderIdentityIsExplicitAndAliasesDoNotSelectIt(t *testing.T) {
	cfg := Config{Version: 1, Routing: Routing{Primary: "arbitrary-work-profile"}, Profiles: map[string]Profile{
		"arbitrary-work-profile": {Provider: ProviderXiaomiMiMo, BaseURL: "https://provider.example/v1", Model: "custom-model-id"},
	}}
	route, err := ResolveRouting(cfg, Overrides{})
	if err != nil || route.Primary.Profile.Provider != ProviderXiaomiMiMo || route.Primary.Profile.MaximumOutputTokens != 131072 {
		t.Fatalf("provider identity was lost: %+v %v", route, err)
	}
	encoded, err := json.Marshal(route)
	if err != nil {
		t.Fatal(err)
	}
	var saved Route
	if err = json.Unmarshal(encoded, &saved); err != nil || saved.Primary.Profile.Provider != ProviderXiaomiMiMo {
		t.Fatalf("saved route lost provider identity: %v", err)
	}
	p := cfg.Profiles[cfg.Routing.Primary]
	p.Provider = ProviderKind("unknown-provider")
	cfg.Profiles[cfg.Routing.Primary] = p
	if err = cfg.Validate(); err == nil {
		t.Fatal("unknown provider identity was accepted")
	}
}

func TestProviderIdentityRejectsUnknownAndNoncanonicalValues(t *testing.T) {
	for _, raw := range []string{`null`, `1`, `"unknown"`, `"XIAOMI-MIMO"`, `"999"`} {
		var value ProviderKind
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("accepted invalid provider %s", raw)
		}
	}
}

func TestXiaomiMiMoDefaultURLAndExplicitOverride(t *testing.T) {
	cfg := Config{Version: 1, Routing: Routing{Primary: "work"}, Profiles: map[string]Profile{
		"work": {Provider: ProviderXiaomiMiMo, Model: "chosen-model"},
	}}
	route, err := ResolveRouting(cfg, Overrides{})
	if err != nil || route.Primary.Profile.BaseURL != "https://token-plan-sgp.xiaomimimo.com/v1" {
		t.Fatalf("wrong default MiMo URL: %+v %v", route, err)
	}
	route, err = ResolveRouting(cfg, Overrides{BaseURL: "https://custom.example/v1/"})
	if err != nil || route.Primary.Profile.BaseURL != "https://custom.example/v1" {
		t.Fatalf("explicit URL was replaced: %+v %v", route, err)
	}
}
