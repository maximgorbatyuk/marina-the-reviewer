package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/buildinfo"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/tui"
)

func Run(args []string, in io.Reader, out, errOut io.Writer) int {
	return RunContext(context.Background(), args, in, out, errOut)
}

func RunContext(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) > 0 && args[0] == "usage" {
		return usageCommand(args[1:], out, errOut)
	}
	if len(args) == 0 || args[0] == "--config" || strings.HasPrefix(args[0], "--config=") {
		fs := flag.NewFlagSet("marinadoreview", flag.ContinueOnError)
		fs.SetOutput(errOut)
		path := fs.String("config", "", "provider configuration path")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() != 0 {
			fmt.Fprintln(errOut, "usage: marinadoreview [--config PATH]")
			return 2
		}
		resolved, err := configPath(*path)
		if err == nil {
			err = tui.Run(ctx, resolved, in, out)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 130
		}
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
		return 0
	}
	if args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(out, "marinadoreview — prepare, run and validate AI code reviews\n\nRun without arguments to open the configuration TUI (terminal required).\nUse --config PATH to open a selected configuration.\n\nCommands: prepare, plan, run, review, validate, archive, compat, config, prompts, capabilities, usage")
		return 0
	}
	if args[0] == "--version" || args[0] == "version" {
		if len(args) > 1 && args[1] == "--json" {
			if err := json.NewEncoder(out).Encode(map[string]string{"tool": "marina-the-reviewer", "command": "marinadoreview", "version": buildinfo.Version, "commit": buildinfo.Commit, "date": buildinfo.Date}); err != nil {
				fmt.Fprintln(errOut, err)
				return 5
			}
		} else {
			fmt.Fprintf(out, "marinadoreview %s (%s)\n", buildinfo.Version, buildinfo.Commit)
		}
		return 0
	}
	return execute(ctx, args, in, out, errOut)
}
