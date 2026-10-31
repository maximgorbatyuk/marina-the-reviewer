package engine

import "github.com/maximgorbatyuk/marina-the-reviewer/internal/redact"

func redactText(value string, tokens map[string]string) string { return redact.Text(value, tokens) }
