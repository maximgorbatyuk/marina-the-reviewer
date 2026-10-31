// Package cli keeps command parsing and process exit policy separate from review execution.
package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func validateArchivePaths(command string, a arguments) error {
	if a.archiveOutput != "" && command != "run" && command != "review" {
		return errors.New("--archive-output requires run or review")
	}
	if command == "archive" && a.runDir == "" {
		return errors.New("archive requires --run-dir")
	}
	if a.archiveOutput == "-" {
		return errors.New("execution archive requires a private output file")
	}
	outputs := []string{a.archiveOutput}
	if command == "archive" {
		outputs = append(outputs, a.output)
	}
	for _, output := range outputs {
		if output == "" || output == "-" {
			continue
		}
		path, err := physicalArchivePath(output)
		if err != nil {
			return err
		}
		inputs := []string{a.input, a.eventsFile}
		if command != "archive" {
			inputs = append(inputs, a.output)
		}
		if a.runDir != "" {
			for _, name := range []string{"plan.json", "state.json", "events.jsonl", "writer.lock"} {
				inputs = append(inputs, filepath.Join(a.runDir, name))
			}
		}
		for _, input := range inputs {
			if input == "" || input == "-" {
				continue
			}
			other, err := physicalArchivePath(input)
			if err != nil {
				return err
			}
			if path == other {
				return errors.New("execution archive requires a separate output file")
			}
		}
	}
	return nil
}

func physicalArchivePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		directory, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// Resolve links before cleaning parent references such as alias/../state.json.
		path = directory + string(os.PathSeparator) + path
	}
	missing := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent, name := filepath.Split(strings.TrimRight(path, string(os.PathSeparator)))
		if parent == "" || parent == path {
			return "", err
		}
		missing = append(missing, name)
		path = parent
	}
}
