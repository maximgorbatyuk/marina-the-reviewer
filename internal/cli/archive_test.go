package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveCapabilityIsExplicit(t *testing.T) {
	var output, diagnostic bytes.Buffer
	if code := Run([]string{"capabilities", "--json"}, &bytes.Buffer{}, &output, &diagnostic); code != 0 {
		t.Fatalf("capabilities: %d %s", code, diagnostic.String())
	}
	var capability struct {
		ExecutionArchiveVersion int `json:"executionArchiveVersion"`
	}
	if err := json.Unmarshal(output.Bytes(), &capability); err != nil || capability.ExecutionArchiveVersion != 1 {
		t.Fatal("execution archive capability is missing")
	}
}

func TestArchiveOutputCannotAliasProtectedPaths(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "run")
	if err := os.MkdirAll(filepath.Join(directory, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Join(directory, "nested"), alias); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plan.json", "state.json", "events.jsonl", "writer.lock", "input.json", "result.json", "events-export.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, name)
			if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			output := alias + string(os.PathSeparator) + ".." + string(os.PathSeparator) + name
			for _, command := range []string{"archive", "run", "review"} {
				if command == "archive" && name == "result.json" {
					continue
				}
				a := arguments{runDir: directory, input: filepath.Join(directory, "input.json"), eventsFile: filepath.Join(directory, "events-export.json"), output: filepath.Join(directory, "result.json"), archiveOutput: output}
				if command == "archive" {
					a.output, a.archiveOutput = output, ""
				}
				if err := validateArchivePaths(command, a); err == nil {
					t.Errorf("%s accepted an alias of a protected path", command)
				}
			}
		})
	}
}

func TestArchiveOutputAllowsNewDirectoriesThroughAlias(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	a := arguments{runDir: filepath.Join(root, "new-run"), archiveOutput: filepath.Join(alias, "new", "nested", "archive.json"), output: "-"}
	if err := validateArchivePaths("run", a); err != nil {
		t.Fatalf("separate new output was rejected: %v", err)
	}
}

func TestArchiveOutputCannotReplaceRunState(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"plan.json", "state.json", "events.jsonl", "writer.lock"} {
		var output, diagnostic bytes.Buffer
		code := Run([]string{"archive", "--run-dir", directory, "--output", filepath.Join(directory, name)}, &bytes.Buffer{}, &output, &diagnostic)
		if code != 2 || output.Len() != 0 {
			t.Fatalf("unsafe archive output %s: %d %s", name, code, diagnostic.String())
		}
	}
}
