package cli

import (
	"strings"
	"testing"
	"time"
)

func TestTerminalWaitForOutput(t *testing.T) {
	t.Run("ignores earlier output", func(t *testing.T) {
		s := &terminalSession{}
		s.output.WriteString("\x1b[36m> Usage\x1b[0m\n")
		start := len(s.text())
		s.output.WriteString("Usage — tokens and code reviews\n")
		if err := s.waitForOutput("> Usage", start, 0); err == nil {
			t.Fatal("wait accepted the previous home screen")
		}
		s.output.WriteString("\x1b[36m> Usage\x1b[0m\n")
		if err := s.waitForOutput("> Usage", start, 0); err != nil {
			t.Fatalf("wait rejected the new home screen: %v", err)
		}
	})

	t.Run("timeout includes full terminal output", func(t *testing.T) {
		s := &terminalSession{}
		s.output.WriteString("previous frame\n")
		start := len(s.text())
		s.output.WriteString("\x1b[31mcurrent frame\x1b[0m\n")
		err := s.waitForOutput("TEST-EXIT:0", start, 0)
		if err == nil {
			t.Fatal("wait succeeded without the exit marker")
		}
		for _, want := range []string{"terminal did not show", "TEST-EXIT:0", "terminal output:\nprevious frame\ncurrent frame\n"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("timeout diagnostic %q does not contain %q", err, want)
			}
		}
	})

	t.Run("process exit includes terminal output", func(t *testing.T) {
		s := &terminalSession{done: make(chan struct{})}
		s.output.WriteString("terminal stopped\n")
		close(s.done)
		err := s.waitForOutput("TEST-EXIT:0", 0, 15*time.Second)
		if err == nil {
			t.Fatal("wait succeeded without the exit marker")
		}
		for _, want := range []string{"terminal exited before", "TEST-EXIT:0", "terminal output:\nterminal stopped\n"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("exit diagnostic %q does not contain %q", err, want)
			}
		}
	})
}
