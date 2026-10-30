package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveRoundTripAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	cfg, revision, err := OpenForEdit(path)
	if err != nil || revision != "" || cfg.Version != 1 {
		t.Fatalf("new configuration: %v", err)
	}
	cfg = example()
	p := cfg.Profiles["main"]
	p.ExtraParameters = map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"low"`)}
	p.Stream = true
	cfg.Profiles["main"] = p
	cfg.Routing.FallbackOn = []string{"timeout"}
	next, err := Save(path, cfg, revision)
	if err != nil || next == "" {
		t.Fatalf("save: %v", err)
	}
	loaded, saved, err := OpenForEdit(path)
	if err != nil || saved != next || !reflect.DeepEqual(loaded, cfg) {
		t.Fatalf("round trip changed configuration: %v", err)
	}
	for _, entry := range []struct {
		path string
		mode os.FileMode
	}{{path, 0600}, {filepath.Dir(path), 0700}} {
		info, err := os.Stat(entry.path)
		if err != nil || info.Mode().Perm() != entry.mode {
			t.Fatalf("private mode: %v", err)
		}
	}
	changed := cfg
	changed.Routing.Fallback = ""
	if _, err := Save(path, changed, next); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(path, cfg, next); err == nil {
		t.Fatal("stale edit overwrote file")
	}
	loaded, err = Load(path)
	if err != nil || loaded.Routing.Fallback != "" {
		t.Fatal("conflict changed file")
	}
}

func TestSaveRejectsInvalidOrUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"corrupt", "symlink", "directory", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			cfg := example()
			switch kind {
			case "corrupt":
				if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "invalid":
				cfg.Routing.Primary = "missing"
			}
			if _, err := Save(path, cfg, ""); err == nil {
				t.Fatal("unsafe save accepted")
			}
			if kind == "corrupt" {
				data, _ := os.ReadFile(path)
				if string(data) != "broken" {
					t.Fatal("corrupt file replaced")
				}
			}
			if kind == "invalid" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("invalid config was written")
				}
			}
		})
	}
}

func TestResolveDraftProfileWithoutRouting(t *testing.T) {
	cfg := example()
	cfg.Routing = Routing{}
	p, err := ResolveProfile(cfg, "main")
	if err != nil || p.Profile.TimeoutSeconds == 0 || p.Profile.APIKey != "" {
		t.Fatal("draft profile was not normalized")
	}
	token, err := ResolveToken(p, cfg, nil)
	if err != nil || token != cfg.Profiles["main"].APIKey {
		t.Fatal("draft credential unavailable")
	}
	if _, err := ResolveProfile(cfg, "absent"); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("draft weakened routing validation")
	}
}
