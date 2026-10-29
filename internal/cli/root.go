package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/buildinfo"
)

func Run(args []string, in io.Reader, out, errOut io.Writer) int {
	return RunContext(context.Background(), args, in, out, errOut)
}

func RunContext(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(out, "marinadoreview — prepare, run and validate AI code reviews\n\nCommands: prepare, plan, run, review, validate, compat, config, prompts, capabilities")
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
