package cli

import (
	"context"
	"encoding/json"
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
)

func TestTerminalProcess(t *testing.T) {
	if os.Getenv("MARINADOREVIEW_TEST_TERMINAL") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("MARINADOREVIEW_TEST_ARGS")), &args); err != nil {
		os.Exit(99)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	code := RunContext(ctx, args, os.Stdin, os.Stdout, os.Stderr)
	fmt.Fprintf(os.Stdout, "\nTEST-EXIT:%d\n", code)
	// Keep the session leader alive until the parent checks the terminal mode.
	// macOS can revoke the terminal when the session leader exits.
	io.ReadFull(os.Stdin, make([]byte, 1))
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
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminalProcess$")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "MARINADOREVIEW_TEST_TERMINAL=1", "MARINADOREVIEW_TEST_ARGS="+string(encoded), "MARINADOREVIEW_CONFIG="+path, "GORACE=atexit_sleep_ms=0")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	s := &terminalSession{master: master, slave: slave, cmd: cmd, changed: make(chan struct{}, 1), done: make(chan struct{}), before: before}
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
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
			cmd.Process.Kill()
			<-s.done
		}
		master.Close()
		slave.Close()
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
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		if strings.Contains(ansi.Strip(s.text()), value) {
			return
		}
		select {
		case <-s.changed:
		case <-s.done:
			t.Fatalf("terminal exited before %q: %v", value, s.err)
		case <-timer.C:
			t.Fatalf("terminal did not show %q", value)
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
		t.Fatal("terminal did not exit")
	}
	actual := 0
	if s.err != nil {
		if exit, ok := s.err.(*exec.ExitError); ok {
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
			fmt.Fprint(w, `{"data":[{"id":"review-model"}]}`)
			return
		}
		probes.Add(1)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]}`)
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
}

func TestTUIInterruptCancelsRequestAndRestoresTerminal(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
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
