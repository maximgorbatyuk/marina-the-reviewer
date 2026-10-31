package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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

func TestBundleReviewUsesOneCommandAndNeverRepeatsCompletedInference(t *testing.T) {
	t.Setenv("MTR_BUNDLE_TEST_TOKEN", "synthetic-first-token")
	directory := t.TempDir()
	repository := filepath.Join(directory, "source")
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", repository}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		data, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s: %v", data, err)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "--initial-branch=dev")
	path := filepath.Join(repository, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte("package main\nfunc main() { println(1) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "head")
	head := git("rev-parse", "HEAD")
	var calls atomic.Int32
	var requestBody atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		requestBody.Store(string(data))
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err = json.Unmarshal(data, &request); err != nil {
			t.Error(err)
			return
		}
		var input struct {
			Batch struct {
				Blocks []struct {
					ID string `json:"id"`
				} `json:"blocks"`
			} `json:"batch"`
		}
		if err = json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
			t.Error(err)
			return
		}
		ids := []string{}
		for _, block := range input.Batch.Blocks {
			ids = append(ids, block.ID)
		}
		model, _ := json.Marshal(map[string]any{"checkedBlockIds": ids, "findings": []any{}, "strengths": []any{}, "unconfirmedDependencies": []any{}})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(model)}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	configFile := filepath.Join(directory, "provider.json")
	config := []byte(`{"version":1,"routing":{"primary":"local"},"profiles":{"local":{"baseUrl":"` + server.URL + `","model":"fake","apiKeyEnv":"MTR_BUNDLE_TEST_TOKEN","contextLength":65536}}}`)
	if err := os.WriteFile(configFile, config, 0600); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(directory, "policy.txt")
	if err := os.WriteFile(policy, []byte("Review this change."), 0600); err != nil {
		t.Fatal(err)
	}
	artifacts := filepath.Join(directory, "bundle")
	args := []string{"review", "--repo", repository, "--base-sha", base, "--start-sha", base, "--head-sha", head, "--config", configFile, "--no-fallback", "--system-prompt-file", policy, "--artifacts-dir", artifacts}
	invoke := func(arguments []string) engine.ArtifactBundle {
		t.Helper()
		var output, diagnostic bytes.Buffer
		if code := Run(arguments, &bytes.Buffer{}, &output, &diagnostic); code != 0 {
			t.Fatalf("exit %d: %s", code, diagnostic.String())
		}
		var result engine.ArtifactBundle
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := invoke(args)
	t.Setenv("MTR_BUNDLE_TEST_TOKEN", "synthetic-rotated-token")
	second := invoke(args)
	if calls.Load() != 1 || first.RunID != second.RunID || first.PassID != second.PassID || first.PlanHash != second.PlanHash || first.PlanFileSha256 != second.PlanFileSha256 {
		t.Fatal("resume changed identity or repeated inference")
	}
	bundle, err := engine.OpenBundle(artifacts, "")
	if err != nil {
		t.Fatal(err)
	}
	var archive engine.BundleArchive
	if err = bundle.Content.Decode(bundle.Manifest.Entries["archive"], &archive); err != nil {
		t.Fatal(err)
	}
	var attempt struct {
		RequestPayloadRef json.RawMessage `json:"requestPayloadRef"`
	}
	if err = json.Unmarshal(archive.Attempts[0], &attempt); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(archive.Attempts[0], &fields)
	var ref runstore.ContentRef
	_ = json.Unmarshal(attempt.RequestPayloadRef, &ref)
	actual, ok := requestBody.Load().(string)
	if !ok {
		t.Fatal("request body was not captured as text")
	}
	expectedHash := sha256.Sum256([]byte(actual))
	stored, err := bundle.Content.Bytes(ref)
	if err != nil || string(stored) != actual || ref.Sha256 != hex.EncodeToString(expectedHash[:]) {
		t.Fatal("actual HTTP payload was not retained byte for byte", err)
	}
	if _, exists := fields["requestPayload"]; exists {
		t.Fatal("attempt copied the HTTP payload")
	}
	if err = os.WriteFile(policy, []byte("A changed policy."), 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic bytes.Buffer
	if code := Run(args, &bytes.Buffer{}, &output, &diagnostic); code == 0 || calls.Load() != 1 {
		t.Fatal("changed invocation caused a new request")
	}
	t.Setenv("MTR_BUNDLE_TEST_TOKEN", "")
	recovered := invoke([]string{"archive", "--artifacts-dir", artifacts})
	if recovered.Status != "complete" || calls.Load() != 1 {
		t.Fatal("offline archive required credentials or repeated inference")
	}
	exported := filepath.Join(directory, "exported")
	invoke([]string{"archive", "--artifacts-dir", artifacts, "--export-dir", exported})
	if _, err = os.Stat(filepath.Join(exported, "execution")); !os.IsNotExist(err) {
		t.Fatal("public export copied private execution state")
	}
	if err = os.WriteFile(policy, []byte("Review this change."), 0600); err != nil {
		t.Fatal(err)
	}
	copyArgs := append([]string{}, args...)
	copyArgs[len(copyArgs)-1] = exported
	copyResult := invoke(copyArgs)
	if calls.Load() != 1 || copyResult.RunID != first.RunID || copyResult.PlanFileSha256 != first.PlanFileSha256 {
		t.Fatal("public bundle replay repeated inference or changed its plan")
	}
	changedLimits := append(append([]string{}, args...), "--max-bundle-parts", "4097")
	output.Reset()
	diagnostic.Reset()
	if code := Run(changedLimits, &bytes.Buffer{}, &output, &diagnostic); code == 0 || calls.Load() != 1 {
		t.Fatal("changed resource policy was accepted")
	}
}

func TestBundlePrepareFailureKeepsPinnedIdentityAndOriginalDiagnostic(t *testing.T) {
	directory := t.TempDir()
	repository := filepath.Join(directory, "objects.git")
	command := exec.CommandContext(t.Context(), "git", "init", "--bare", repository)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", data, err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	config := filepath.Join(directory, "provider.json")
	if err := os.WriteFile(config, []byte(`{"version":1,"routing":{"primary":"local"},"profiles":{"local":{"baseUrl":"`+server.URL+`","model":"fake","contextLength":65536}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	artifacts := filepath.Join(directory, "bundle")
	var output, diagnostic bytes.Buffer
	code := Run([]string{"review", "--repo", repository, "--base-sha", base, "--start-sha", base, "--head-sha", head, "--config", config, "--artifacts-dir", artifacts}, &bytes.Buffer{}, &output, &diagnostic)
	if code == 0 || requests.Load() != 0 {
		t.Fatal("missing source caused inference or success")
	}
	data, err := os.ReadFile(filepath.Join(artifacts, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest engine.ArtifactBundle
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.BaseSha != base || manifest.StartSha != base || manifest.HeadSha != head || manifest.Status != "prepared" || manifest.RunID == "" || manifest.PassID == "" {
		t.Fatal("failed preparation lost its invocation identity")
	}
	if _, exists := manifest.Entries["snapshot"]; exists {
		t.Fatal("failed preparation invented a source snapshot")
	}
	var failure struct {
		Phase   string `json:"phase"`
		Message string `json:"message"`
	}
	lines := bytes.Split(bytes.TrimSpace(diagnostic.Bytes()), []byte{'\n'})
	if len(lines) == 0 || json.Unmarshal(lines[len(lines)-1], &failure) != nil || failure.Phase != "prepare" || failure.Message == "" || strings.Contains(failure.Message, "corrupt") {
		t.Fatalf("original preparation error was hidden: %s", diagnostic.String())
	}
}
