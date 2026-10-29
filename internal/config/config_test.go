package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func example() Config {
	return Config{Version: 1, Routing: Routing{Primary: "main", Fallback: "backup"}, Profiles: map[string]Profile{
		"main":   {BaseURL: "https://primary.example/v1", Model: "main-model", APIKey: "secret-main"},
		"backup": {BaseURL: "https://backup.example/v1", Model: "backup-model", APIKeyEnv: "BACKUP_KEY"},
		"unused": {BaseURL: "http://127.0.0.1:1234/v1", Model: "local"},
	}}
}

func TestConfigWithLiteralTokenMustBePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(example())
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("public credential file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCredentialOverrideIsPrimaryOnly(t *testing.T) {
	c := example()
	r, e := ResolveRouting(c, Overrides{})
	if e != nil {
		t.Fatal(e)
	}
	token, e := ResolveToken(r.Primary, c, map[string]string{r.Primary.Name: "override"})
	if e != nil || token != "override" {
		t.Fatal("runtime credential was ignored")
	}
	t.Setenv("BACKUP_KEY", "backup-value")
	token, e = ResolveToken(*r.Fallback, c, map[string]string{r.Primary.Name: "override"})
	if e != nil || token != "backup-value" {
		t.Fatal("runtime credential leaked into fallback")
	}
}

func TestUnauthenticatedSnapshotDoesNotDependOnConfigAtRunTime(t *testing.T) {
	c := Config{Version: 1, Routing: Routing{Primary: "local"}, Profiles: map[string]Profile{"local": {BaseURL: "http://127.0.0.1:1234", Model: "local"}}}
	r, e := ResolveRouting(c, Overrides{})
	if e != nil {
		t.Fatal(e)
	}
	if token, e := ResolveToken(r.Primary, Config{}, nil); e != nil || token != "" {
		t.Fatalf("local plan requires configuration: %v", e)
	}
}
func TestRoutingSnapshotsHideKeys(t *testing.T) {
	c := example()
	r, err := ResolveRouting(c, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Primary.Name != "main" || r.Fallback == nil || r.Fallback.Name != "backup" {
		t.Fatalf("bad route: %+v", r)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "secret-main") {
		t.Fatal("secret in route")
	}
	token, err := ResolveToken(r.Primary, c, r.RuntimeSecrets)
	if err != nil || token != "secret-main" {
		t.Fatalf("token: %q %v", token, err)
	}
}
func TestEndpointOverrideCannotReuseSavedKey(t *testing.T) {
	c := example()
	r, err := ResolveRouting(c, Overrides{BaseURL: "https://other.example/v1"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := ResolveToken(r.Primary, c, r.RuntimeSecrets)
	if err == nil && token == "secret-main" {
		t.Fatal("credential sent to changed endpoint")
	}
}
func TestExplicitEnvironmentOverrideAndFallbackIsolation(t *testing.T) {
	t.Setenv("ONCE_KEY", "once-secret")
	t.Setenv("BACKUP_KEY", "backup-secret")
	c := example()
	r, err := ResolveRouting(c, Overrides{BaseURL: "https://other.example/v1", APIKeyEnv: "ONCE_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := ResolveToken(r.Primary, c, r.RuntimeSecrets)
	if err != nil || a != "once-secret" {
		t.Fatalf("primary %q %v", a, err)
	}
	b, err := ResolveToken(*r.Fallback, c, r.RuntimeSecrets)
	if err != nil || b != "backup-secret" {
		t.Fatalf("backup %q %v", b, err)
	}
}
func TestInvalidRolesAndReservedParameters(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.Routing.Fallback = "main" }, func(c *Config) { c.Routing.Primary = "missing" }, func(c *Config) {
		p := c.Profiles["main"]
		p.ExtraParameters = map[string]json.RawMessage{"messages": json.RawMessage(`[]`)}
		c.Profiles["main"] = p
	}} {
		c := example()
		change(&c)
		if _, err := ResolveRouting(c, Overrides{}); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
func TestDisableFallbackConflictsWithSelection(t *testing.T) {
	c := example()
	r, err := ResolveRouting(c, Overrides{NoFallback: true})
	if err != nil || r.Fallback != nil {
		t.Fatalf("route: %+v %v", r, err)
	}
	if _, err := ResolveRouting(c, Overrides{NoFallback: true, Fallback: "backup"}); err == nil {
		t.Fatal("conflicting flags accepted")
	}
}
func TestInitPreservesExistingConfigAndUsesPrivateMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "private", "config.json")
	if err := Init(p); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", st.Mode())
	}
	original, _ := os.ReadFile(p)
	if err := Init(p); err == nil {
		t.Fatal("overwrote config")
	}
	after, _ := os.ReadFile(p)
	if string(original) != string(after) {
		t.Fatal("existing config changed")
	}
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
}
