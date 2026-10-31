package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/engine"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
)

func TestGitToReviewAndResume(t *testing.T) {
	t.Setenv("MTR_TEST_CLI_KEY", "synthetic-test-key")
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %s: %v", b, e)
		}
	}
	git("init", "--initial-branch=dev")
	path := filepath.Join(repo, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Error(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	if err := os.WriteFile(path, []byte("package main\nfunc main() { println(1) }\n"), 0600); err != nil {
		t.Error(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "head")
	resolve := func(ref string) string {
		t.Helper()
		data, err := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", ref).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data))
	}
	baseSha, headSha := resolve("HEAD~1"), resolve("HEAD")
	bare := filepath.Join(dir, "objects.git")
	if data, err := exec.CommandContext(t.Context(), "git", "clone", "--bare", "--no-hardlinks", repo, bare).CombinedOutput(); err != nil {
		t.Fatalf("bare source: %s %v", data, err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
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
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(model)}, "finish_reason": "stop"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	conf := filepath.Join(dir, "config.json")
	if err := os.WriteFile(conf, []byte(`{"version":1,"routing":{"primary":"local"},"profiles":{"local":{"baseUrl":"`+server.URL+`","model":"fake","apiKeyEnv":"MTR_TEST_CLI_KEY","contextLength":65536}}}`), 0600); err != nil {
		t.Error(err)
	}
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
	archivePath := filepath.Join(dir, "archive.json")
	invoke("prepare", "--repo", bare, "--base-sha", baseSha, "--start-sha", baseSha, "--head-sha", headSha, "--output", snapshot)
	invoke("plan", "--input", snapshot, "--config", conf, "--output", plan)
	result := invoke("run", "--input", plan, "--config", conf, "--run-dir", runDir, "--archive-output", archivePath)
	if result["status"] != "complete" {
		t.Fatalf("result: %v", result)
	}
	invoke("run", "--input", plan, "--config", conf, "--run-dir", runDir)
	if calls.Load() != 1 {
		t.Fatalf("calls %d", calls.Load())
	}
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	var archive map[string]any
	if err = json.Unmarshal(archiveBytes, &archive); err != nil || archive["kind"] != "ReviewExecutionArchive" || archive["runId"] != result["runId"] || archive["planFileSha256"] == "" {
		t.Fatalf("execution archive: %v", err)
	}
	planBytes, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	executionPlan, err := os.ReadFile(filepath.Join(runDir, "plan.json"))
	if err != nil || !bytes.Equal(planBytes, executionPlan) {
		t.Fatal("execution changed the saved plan bytes")
	}
	planDigest := sha256.Sum256(planBytes)
	if archive["planFileSha256"] != hex.EncodeToString(planDigest[:]) {
		t.Fatal("archive plan file hash differs from saved plan bytes")
	}
	marinaPlan := invoke("compat", "export", "--input", plan, "--format", "marina-v3")
	if marinaPlan["version"] != float64(3) || marinaPlan["kind"] != nil {
		t.Fatal("plan export is not Marina v3")
	}
	batch := invoke("compat", "export", "--input", plan, "--batch", "1", "--format", "marina-v3")
	if batch["input"] == nil || batch["inputSha256"] == nil {
		t.Fatal("batch export missing input identity")
	}
	t.Setenv("MTR_TEST_CLI_KEY", "")
	recovered := invoke("archive", "--run-dir", runDir)
	if recovered["planHash"] != archive["planHash"] || calls.Load() != 1 {
		t.Fatal("offline archive changed the plan or executed inference")
	}
	var missingOut, missingErr bytes.Buffer
	if code := Run([]string{"run", "--input", plan, "--config", conf, "--run-dir", filepath.Join(dir, "missing-credentials")}, &bytes.Buffer{}, &missingOut, &missingErr); code != 2 {
		t.Fatalf("missing credentials exit %d: %s", code, missingErr.String())
	}
	if calls.Load() != 1 || missingOut.Len() != 0 {
		t.Fatal("missing credentials caused an inference or result output")
	}
	t.Run("aliased-run-state", func(t *testing.T) {
		alias := filepath.Join(dir, "run-alias")
		if err := os.Symlink(runDir, alias); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"plan.json", "state.json", "events.jsonl", "writer.lock"} {
			path := filepath.Join(runDir, name)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var out, diagnostic bytes.Buffer
			if code := Run([]string{"archive", "--run-dir", runDir, "--output", filepath.Join(alias, name)}, &bytes.Buffer{}, &out, &diagnostic); code != 2 || out.Len() != 0 {
				t.Fatalf("aliased %s was not rejected: exit %d %s", name, code, diagnostic.String())
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected output changed %s", name)
			}
		}
	})
	t.Run("large-offline-archive", func(t *testing.T) {
		planHash, ok := archive["planHash"].(string)
		if !ok {
			t.Fatal("archive has no plan hash")
		}
		store, err := runstore.Open(runDir, planHash)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		var saved map[string]json.RawMessage
		if err = store.Read(&saved); err != nil {
			t.Fatal(err)
		}
		var attempts []engine.ArchiveAttempt
		if err = json.Unmarshal(saved["archiveAttempts"], &attempts); err != nil || len(attempts) != 1 {
			t.Fatalf("saved archive attempts: %v", err)
		}
		attempts[0].RawOutput = strings.Repeat("x", 65<<20)
		if saved["archiveAttempts"], err = json.Marshal(attempts); err != nil {
			t.Fatal(err)
		}
		if err = store.Write(saved); err != nil {
			t.Fatal(err)
		}
		store.Close()
		var out, diagnostic bytes.Buffer
		if code := Run([]string{"archive", "--run-dir", runDir, "--output", archivePath}, &bytes.Buffer{}, &out, &diagnostic); code != 0 {
			t.Fatalf("large offline archive exit %d: %s", code, diagnostic.String())
		}
		data, err := runstore.ReadPrivate(archivePath, runstore.MaximumExecutionDocumentBytes)
		if err != nil || len(data) <= runstore.MaximumDocumentBytes {
			t.Fatalf("execution archive did not use its separate bound: %v", err)
		}
		var recovered struct {
			Attempts []struct {
				RawOutput string `json:"rawOutput"`
			} `json:"attempts"`
		}
		if err = json.Unmarshal(data, &recovered); err != nil || len(recovered.Attempts) != 1 || len(recovered.Attempts[0].RawOutput) != 65<<20 || calls.Load() != 1 || out.Len() != 0 {
			t.Fatalf("large offline archive changed its output or executed inference: %v", err)
		}
	})
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
