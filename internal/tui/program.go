package tui

import (
	"context"
	"errors"
	"io"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
)

func Run(ctx context.Context, path string, in io.Reader, out io.Writer) error {
	input, okIn := in.(interface{ Fd() uintptr })
	output, okOut := out.(interface{ Fd() uintptr })
	if !okIn || !okOut || !term.IsTerminal(input.Fd()) || !term.IsTerminal(output.Fd()) {
		return errors.New("TUI requires a terminal on stdin and stdout; use --help or a CLI subcommand")
	}
	m := newModel(ctx, path)
	defer m.cancelNetwork()
	program := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out))
	_, err := program.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if m.interrupted {
		return context.Canceled
	}
	return err
}
