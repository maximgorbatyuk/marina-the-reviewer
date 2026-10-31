package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func TestTerminalProcess(_ *testing.T) {
	if os.Getenv("MARINADOREVIEW_TEST_TERMINAL") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("MARINADOREVIEW_TEST_ARGS")), &args); err != nil {
		os.Exit(99)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	recorder := usage.NewRecorder(filepath.Join(filepath.Dir(os.Getenv("MARINADOREVIEW_CONFIG")), "stats", "stats.sqlite3"))
	ctx = usage.WithRecorder(ctx, recorder)
	code := RunContext(ctx, args, os.Stdin, os.Stdout, os.Stderr)
	if err := recorder.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "Test usage recording failed:", err)
		os.Exit(98)
	}
	_, _ = fmt.Fprintf(os.Stdout, "\nTEST-EXIT:%d\n", code)
	// Keep the session leader alive until the parent checks the terminal mode.
	// macOS can revoke the terminal when the session leader exits.
	_, _ = io.ReadFull(os.Stdin, make([]byte, 1))
	os.Exit(code)
}

type terminalSession struct {
	master, slave *os.File
	cmd           *exec.Cmd
	mu            sync.Mutex
	output        strings.Builder
	changed       chan struct{}
	done          chan struct{}
	err           error
	before        *term.State
}

func startTerminal(t *testing.T, path string, args []string) *terminalSession {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(args)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestTerminalProcess$")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "MARINADOREVIEW_TEST_TERMINAL=1", "MARINADOREVIEW_TEST_ARGS="+string(encoded), "MARINADOREVIEW_CONFIG="+path, "GORACE=atexit_sleep_ms=0")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	s := &terminalSession{master: master, slave: slave, cmd: cmd, changed: make(chan struct{}, 1), done: make(chan struct{}), before: before}
	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		t.Fatal(err)
	}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				s.mu.Lock()
				s.output.Write(buffer[:n])
				s.mu.Unlock()
				select {
				case s.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { s.err = cmd.Wait(); close(s.done) }()
	t.Cleanup(func() {
		select {
		case <-s.done:
		default:
			_ = cmd.Process.Kill()
			<-s.done
		}
		_ = master.Close()
		_ = slave.Close()
	})
	return s
}

func (s *terminalSession) text() string { s.mu.Lock(); defer s.mu.Unlock(); return s.output.String() }
func (s *terminalSession) send(t *testing.T, value string) {
	t.Helper()
	if _, err := s.master.Write([]byte(value)); err != nil {
		t.Fatal(err)
	}
}
func (s *terminalSession) waitFor(t *testing.T, value string) {
	t.Helper()
	s.waitForSince(t, value, 0)
}
func (s *terminalSession) waitForSince(t *testing.T, value string, start int) {
	t.Helper()
	if err := s.waitForOutput(value, start, 15*time.Second); err != nil {
		t.Fatal(err)
	}
}
func (s *terminalSession) waitForOutput(value string, start int, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if strings.Contains(ansi.Strip(s.text()[start:]), value) {
			return nil
		}
		select {
		case <-s.changed:
		case <-s.done:
			if s.err != nil {
				return fmt.Errorf("terminal exited before %q: %w\nterminal output:\n%s", value, s.err, ansi.Strip(s.text()))
			}
			return fmt.Errorf("terminal exited before %q\nterminal output:\n%s", value, ansi.Strip(s.text()))
		case <-timer.C:
			return fmt.Errorf("terminal did not show %q within %s\nterminal output:\n%s", value, timeout, ansi.Strip(s.text()))
		}
	}
}
func (s *terminalSession) exit(t *testing.T, code int) {
	t.Helper()
	s.waitFor(t, fmt.Sprintf("TEST-EXIT:%d", code))
	after, err := term.GetState(s.slave.Fd())
	if err != nil || !reflect.DeepEqual(s.before, after) {
		t.Fatalf("terminal mode was not restored: %v", err)
	}
	s.send(t, "\n")
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		t.Fatalf("terminal did not exit\nterminal output:\n%s", ansi.Strip(s.text()))
	}
	actual := 0
	if s.err != nil {
		var exit *exec.ExitError
		if errors.As(s.err, &exit) {
			actual = exit.ExitCode()
		} else {
			t.Fatal(s.err)
		}
	}
	if actual != code {
		t.Fatalf("terminal exit %d, want %d", actual, code)
	}
}

func TestTUIBareInvocationConfiguresAndTestsProfile(t *testing.T) {
	var models, probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			models.Add(1)
			_, _ = fmt.Fprint(w, `{"data":[{"id":"review-model"}]}`)
			return
		}
		probes.Add(1)
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	s := startTerminal(t, path, nil)
	s.waitFor(t, "Providers and routing")
	s.send(t, "\r")
	s.waitFor(t, "No profiles.")
	s.send(t, "n")
	s.waitFor(t, "Edit provider profile")
	s.send(t, "\x15cloud\t\r")
	s.waitFor(t, "Select preset")
	s.send(t, "Ollama\r")
	s.waitFor(t, "No authorization")
	s.send(t, "\t\x15"+server.URL+"/v1\t\t\tmanual-model\t\r")
	s.waitFor(t, "Select model")
	s.send(t, "review-model\x1b[B\r\t\t\r")
	s.waitFor(t, "Verified: selected model")
	s.send(t, "\t\t\r")
	s.waitFor(t, "Profile applied to draft.")
	s.send(t, "s")
	s.waitFor(t, "Configuration saved.")
	s.send(t, "q")
	s.exit(t, 0)
	cfg, err := config.Load(path)
	if err != nil || cfg.Routing.Primary != "cloud" || cfg.Profiles["cloud"].Model != "review-model" {
		t.Fatalf("saved config: %v", err)
	}
	if models.Load() != 1 || probes.Load() != 1 {
		t.Fatalf("models %d probes %d", models.Load(), probes.Load())
	}
	report, err := usage.Generate(usage.Store{Path: filepath.Join(filepath.Dir(path), "stats", "stats.sqlite3")}, time.Now().UTC(), []int{7})
	if err != nil || report.Periods[0].Total.Requests != 1 || report.Periods[0].Total.TotalTokens == nil || *report.Periods[0].Total.TotalTokens != 8 {
		t.Fatal("TUI test usage not recorded or model discovery counted as tokens")
	}
}

func TestTUIInterruptCancelsRequestAndRestoresTerminal(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Config{Version: 1, Routing: config.Routing{Primary: "local"}, Profiles: map[string]config.Profile{"local": {BaseURL: server.URL + "/v1", Model: "test-model"}}}
	if _, err := config.Save(path, cfg, ""); err != nil {
		t.Fatal(err)
	}
	s := startTerminal(t, path, []string{"--config", path})
	s.waitFor(t, "Providers and routing")
	s.send(t, "\rt")
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("probe did not start")
	}
	s.send(t, "\x03")
	s.exit(t, 130)
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("request was not canceled")
	}
}

func TestTUIUsagePeriodsReadLocalHistory(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "stats", "stats.sqlite3")
	t.Setenv("MARINADOREVIEW_DB_FILE", journal)
	t.Setenv("MARINADOREVIEW_USAGE_FILE", "")
	now := time.Now().UTC()
	for i, days := range []int{1, 12, 45} {
		at := now.Add(-time.Duration(days) * 24 * time.Hour)
		input, output := int64(10*(i+1)), int64(i+1)
		event := usage.Event{Version: 1, ID: fmt.Sprint(i), Kind: "finish", At: at, StartedAt: at, ProviderID: "https://example.test/v1", Provider: "Example", Profile: "cloud", Model: "model", Operation: "review", Status: "success", Counts: usage.Counts{Input: &input, Output: &output}}
		if err := (usage.Store{Path: journal}).Append(event); err != nil {
			t.Fatal(err)
		}
		finished := at.Add(time.Second)
		if err := (usage.Store{Path: journal}).SaveReview(usage.Review{RunID: fmt.Sprint(i), PassID: "pass", PlanHash: "plan", StartedAt: at, FinishedAt: &finished, Status: "complete"}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	s := startTerminal(t, path, nil)
	s.waitFor(t, "Providers and routing")
	s.send(t, "jj\r")
	s.waitFor(t, "Usage — tokens and code reviews")
	s.waitFor(t, "[1:7d]")
	s.waitFor(t, "Total: 11")
	s.waitFor(t, "Reviews: 1")
	s.send(t, "2")
	s.waitFor(t, "[2:30d]")
	s.waitFor(t, "Total: 33")
	// A resize forces a full frame instead of updates to individual digits.
	if err := pty.Setsize(s.master, &pty.Winsize{Rows: 24, Cols: 81}); err != nil {
		t.Fatal(err)
	}
	s.waitFor(t, "Reviews: 2")
	s.send(t, "3")
	s.waitFor(t, "[3:90d]")
	s.waitFor(t, "Total: 66")
	if err := pty.Setsize(s.master, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	s.waitFor(t, "Reviews: 3")
	s.send(t, "\x1b[6~")
	s.waitFor(t, "Local CLI history only")
	// Wait for a new frame so Esc and q cannot merge into Alt+q.
	homeStart := len(s.text())
	s.send(t, "\x1b")
	s.waitForSince(t, "> Usage", homeStart)
	s.send(t, "q")
	s.exit(t, 0)
	after, err := os.ReadFile(journal)
	if err != nil || string(before) != string(after) {
		t.Fatal("TUI usage changed the journal")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("TUI usage created provider config")
	}
}
