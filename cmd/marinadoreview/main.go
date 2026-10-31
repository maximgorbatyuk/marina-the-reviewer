// Command marinadoreview reviews pinned Git source without changing the reviewed working tree.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/cli"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	recorder := usage.NewRecorder("")
	ctx = usage.WithRecorder(ctx, recorder)
	operation := "tui"
	if len(os.Args) > 1 && os.Args[1] != "--config" && !strings.HasPrefix(os.Args[1], "--config=") {
		operation = os.Args[1]
	}
	ctx = usage.WithOperation(ctx, operation)
	code := cli.RunContext(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	cancel()
	if err := recorder.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: local statistics were not fully recorded:", err)
	}
	os.Exit(code)
}
