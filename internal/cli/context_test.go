package cli

import (
	"bytes"
	"context"
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
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/engine"
)

type cancelPhaseWriter struct {
	bytes.Buffer
	phase  string
	cancel context.CancelFunc
}

func (w *cancelPhaseWriter) Write(raw []byte) (int, error) {
	var event struct{ Phase, Status string }
	if json.Unmarshal(raw, &event) == nil && event.Phase == w.phase && event.Status == "started" {
		w.cancel()
	}
	return w.Buffer.Write(raw)
}

func TestBundleCpuPhaseCancellationDoesNotCommitPlanOrStartInference(t *testing.T) {
	for _, phase := range []string{"analysis", "plan"} {
		t.Run(phase, func(t *testing.T) {
			args, artifacts := cpuCancellationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			diagnostic := &cancelPhaseWriter{phase: phase, cancel: cancel}
			var output bytes.Buffer
			if code := RunContext(ctx, args, strings.NewReader(""), &output, diagnostic); code != 130 {
				t.Fatalf("phase cancellation exit=%d diagnostics=%s", code, diagnostic.String())
			}
			if !strings.Contains(diagnostic.String(), "REVIEW_CANCELLED") {
				t.Fatal(diagnostic.String())
			}
			bundle, err := engine.OpenBundle(artifacts, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := bundle.Manifest.Entries["plan"]; ok {
				t.Fatal("cancelled phase committed a plan")
			}
			if _, err = os.Stat(filepath.Join(artifacts, "plan.json")); !os.IsNotExist(err) {
				t.Fatal("cancelled phase wrote physical plan", err)
			}
			if _, err = os.Stat(filepath.Join(artifacts, "execution")); !os.IsNotExist(err) {
				t.Fatal("cancelled phase started inference", err)
			}
		})
	}
}

func TestBundleExpiredDeadlineReturns124WithoutOpeningInvocation(t *testing.T) {
	args, artifacts := cpuCancellationFixture(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	var output, diagnostic bytes.Buffer
	if code := RunContext(ctx, args, strings.NewReader(""), &output, &diagnostic); code != 124 {
		t.Fatalf("deadline exit=%d diagnostics=%s", code, diagnostic.String())
	}
	if !strings.Contains(diagnostic.String(), "REVIEW_DEADLINE_EXCEEDED") {
		t.Fatal(diagnostic.String())
	}
	if _, err := os.Stat(artifacts); !os.IsNotExist(err) {
		t.Fatal("expired invocation wrote files", err)
	}
}

func TestBundleInferenceCancellationKeepsUnknownAndDoesNotRepeat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer func() {
		close(release)
		server.Close()
	}()
	args, artifacts := cpuCancellationFixture(t)
	config := ""
	for index, arg := range args {
		if arg == "--config" {
			config = args[index+1]
		}
	}
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("http://127.0.0.1:1"), []byte(server.URL))
	if err = os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MTR_CPU_CANCEL_UNUSED", "synthetic-cancel-token")
	var output, diagnostic bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- RunContext(ctx, args, strings.NewReader(""), &output, &diagnostic)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("provider request did not start")
	}
	cancel()
	var code int
	select {
	case code = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CLI did not stop while the provider response was blocked")
	}
	if code != 130 {
		t.Fatalf("inference cancellation exit=%d diagnostics=%s", code, diagnostic.String())
	}
	bundle, err := engine.OpenBundle(artifacts, "")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Status != "unknown" || bundle.Manifest.Entries["archive"].Sha256 == "" || calls.Load() != 1 {
		t.Fatalf("cancelled request evidence changed: status=%s calls=%d", bundle.Manifest.Status, calls.Load())
	}
	output.Reset()
	diagnostic.Reset()
	if code := RunContext(context.Background(), args, strings.NewReader(""), &output, &diagnostic); code != 4 || calls.Load() != 1 {
		t.Fatalf("unknown request repeated: exit=%d calls=%d diagnostic=%s", code, calls.Load(), diagnostic.String())
	}
}

func cpuCancellationFixture(t *testing.T) ([]string, string) {
	t.Helper()
	directory := t.TempDir()
	repository := filepath.Join(directory, "source")
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", repository}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, raw)
		}
		return strings.TrimSpace(string(raw))
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
	config := filepath.Join(directory, "provider.json")
	if err := os.WriteFile(config, []byte(`{"version":1,"routing":{"primary":"local"},"profiles":{"local":{"baseUrl":"http://127.0.0.1:1","model":"fake","apiKeyEnv":"MTR_CPU_CANCEL_UNUSED","contextLength":65536}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	artifacts := filepath.Join(directory, "bundle")
	return []string{"review", "--repo", repository, "--base-sha", base, "--start-sha", base, "--head-sha", head, "--config", config, "--no-fallback", "--artifacts-dir", artifacts}, artifacts
}

var _ io.Writer = (*cancelPhaseWriter)(nil)
