package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGitToReviewAndResume(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %s: %v", b, e)
		}
	}
	git("init", "--initial-branch=dev")
	path := filepath.Join(repo, "main.go")
	os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0600)
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	os.WriteFile(path, []byte("package main\nfunc main() { println(1) }\n"), 0600)
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "head")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		var input struct {
			Batch struct {
				Blocks []struct {
					ID string `json:"id"`
				} `json:"blocks"`
			} `json:"batch"`
		}
		if e := json.Unmarshal([]byte(request.Messages[1].Content), &input); e != nil {
			t.Error(e)
		}
		ids := []string{}
		for _, b := range input.Batch.Blocks {
			ids = append(ids, b.ID)
		}
		model, _ := json.Marshal(map[string]any{"checkedBlockIds": ids, "findings": []any{}, "strengths": []any{}, "unconfirmedDependencies": []any{}})
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(model)}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	conf := filepath.Join(dir, "config.json")
	os.WriteFile(conf, []byte(`{"version":1,"routing":{"primary":"local"},"profiles":{"local":{"baseUrl":"`+server.URL+`","model":"fake","contextLength":65536}}}`), 0600)
	invoke := func(args ...string) map[string]any {
		t.Helper()
		var out, errs bytes.Buffer
		if code := Run(args, &bytes.Buffer{}, &out, &errs); code != 0 {
			t.Fatalf("exit %d: %s\n%s", code, errs.String(), out.String())
		}
		if out.Len() == 0 {
			for i, a := range args {
				if a == "--output" && i+1 < len(args) {
					b, e := os.ReadFile(args[i+1])
					if e != nil {
						t.Fatal(e)
					}
					out.Write(b)
				}
			}
		}
		var v map[string]any
		if e := json.Unmarshal(out.Bytes(), &v); e != nil {
			t.Fatalf("JSON: %v: %s", e, out.String())
		}
		return v
	}
	snapshot := filepath.Join(dir, "snapshot.json")
	plan := filepath.Join(dir, "plan.json")
	runDir := filepath.Join(dir, "run")
	invoke("prepare", "--repo", repo, "--base", "HEAD~1", "--output", snapshot)
	invoke("plan", "--input", snapshot, "--config", conf, "--output", plan)
	result := invoke("run", "--input", plan, "--config", conf, "--run-dir", runDir)
	if result["status"] != "complete" {
		t.Fatalf("result: %v", result)
	}
	invoke("run", "--input", plan, "--config", conf, "--run-dir", runDir)
	if calls.Load() != 1 {
		t.Fatalf("calls %d", calls.Load())
	}
	marinaPlan := invoke("compat", "export", "--input", plan, "--format", "marina-v3")
	if marinaPlan["version"] != float64(3) || marinaPlan["kind"] != nil {
		t.Fatal("plan export is not Marina v3")
	}
	batch := invoke("compat", "export", "--input", plan, "--batch", "1", "--format", "marina-v3")
	if batch["input"] == nil || batch["inputSha256"] == nil {
		t.Fatal("batch export missing input identity")
	}
	providerConfig, _ := os.ReadFile(conf)
	providerConfig = []byte(strings.Replace(string(providerConfig), `"model":"fake"`, `"model":"fake","apiKeyEnv":"MTR_TEST_MISSING_KEY"`, 1))
	os.WriteFile(conf, providerConfig, 0600)
	t.Setenv("MTR_TEST_MISSING_KEY", "")
	var missingOut, missingErr bytes.Buffer
	if code := Run([]string{"run", "--input", plan, "--config", conf, "--run-dir", filepath.Join(dir, "missing-credentials")}, &bytes.Buffer{}, &missingOut, &missingErr); code != 2 {
		t.Fatalf("missing credentials exit %d: %s", code, missingErr.String())
	}
	if calls.Load() != 1 || missingOut.Len() != 0 {
		t.Fatal("missing credentials caused an inference or result output")
	}
}

func TestRunRejectsProviderOverrides(t *testing.T) {
	var out, errs bytes.Buffer
	if code := Run([]string{"run", "--input", "-", "--base-url", "https://different.invalid"}, bytes.NewBufferString(`{}`), &out, &errs); code != 2 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if out.Len() != 0 {
		t.Fatal("usage error polluted stdout")
	}
}
