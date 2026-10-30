package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/ai"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

const (
	fieldName = iota
	fieldURL
	fieldModel
	fieldCredential
	fieldContext
	fieldOutput
	fieldTimeout
	fieldAttempts
	fieldConcurrency
	fieldCount
)

type formRow struct {
	label, action string
	field         int
}

type profileEditor struct {
	name                                          string
	original                                      config.Profile
	fields                                        []textinput.Model
	auth, preset, responseFormat, outputParameter string
	stream, advanced, changed                     bool
	focus                                         int
}

func (e *profileEditor) rows() []formRow {
	nameField := fieldName
	credentialField := fieldCredential
	if e.auth == "none" {
		credentialField = -1
	}
	if e.name != "" {
		nameField = -1
	}
	rows := []formRow{
		{"Profile name", "", nameField}, {"Provider", "preset", -1}, {"API Base URL", "", fieldURL},
		{"Authorization", "auth", -1}, {"Credential", "", credentialField}, {"Model ID (manual)", "", fieldModel},
		{"Load model list", "models", -1}, {"Test connection (uses AI tokens)", "probe", -1}, {"Advanced settings", "advanced", -1},
	}
	if e.advanced {
		rows = append(rows, formRow{"Response format", "format", -1}, formRow{"Output token parameter", "parameter", -1},
			formRow{"Context length", "", fieldContext}, formRow{"Maximum output (0 = auto)", "", fieldOutput},
			formRow{"Request timeout (seconds)", "", fieldTimeout}, formRow{"Maximum attempts", "", fieldAttempts},
			formRow{"Maximum concurrency", "", fieldConcurrency}, formRow{"Stream", "stream", -1})
	}
	return append(rows, formRow{"Apply to draft", "apply", -1}, formRow{"Cancel", "cancel", -1})
}

func (e *profileEditor) focusField(field int) {
	for i, row := range e.rows() {
		if row.field == field {
			e.focus = i
			e.syncFocus()
			return
		}
	}
}

func (e *profileEditor) syncFocus() {
	rows := e.rows()
	e.focus = max(0, min(e.focus, len(rows)-1))
	for i := range e.fields {
		e.fields[i].Blur()
	}
	if i := rows[e.focus].field; i >= 0 {
		e.fields[i].Focus()
	}
}

func authSource(p config.Profile) string {
	switch {
	case p.APIKey != "":
		return "token"
	case p.APIKeyEnv != "":
		return "env"
	case p.APIKeyFile != "":
		return "file"
	default:
		return "none"
	}
}

func (m *model) startEditor(name string) {
	if m.loadFailed {
		m.notice = "Fix and reload the existing configuration first."
		return
	}
	m.cancelNetwork()
	p := ai.Presets()[0].Profile
	preset := "OpenAI"
	if name != "" {
		original := m.cfg.Profiles[name]
		snapshot, err := config.ResolveProfile(m.cfg, name)
		if err != nil {
			m.notice = "Cannot edit profile: " + safeText(err.Error())
			return
		}
		p = snapshot.Profile
		p.APIKey, p.APIKeyEnv, p.APIKeyFile = original.APIKey, original.APIKeyEnv, original.APIKeyFile
		preset = "Other OpenAI-compatible"
		for _, candidate := range ai.Presets() {
			if candidate.Profile.BaseURL != "" && strings.TrimRight(p.BaseURL, "/") == strings.TrimRight(candidate.Profile.BaseURL, "/") {
				preset = candidate.Name
			}
		}
	}
	e := &profileEditor{name: name, original: p, fields: make([]textinput.Model, fieldCount), auth: authSource(p), preset: preset, responseFormat: p.ResponseFormat, outputParameter: p.OutputTokenParameter, stream: p.Stream}
	if name == "" {
		e.auth = "token"
	}
	newName := name
	if newName == "" {
		for i := 1; ; i++ {
			candidate := fmt.Sprintf("profile-%d", i)
			if _, ok := m.cfg.Profiles[candidate]; !ok {
				newName = candidate
				break
			}
		}
	}
	values := []string{newName, p.BaseURL, p.Model, "", strconv.Itoa(p.ContextLength), strconv.Itoa(p.MaximumOutputTokens), strconv.Itoa(p.TimeoutSeconds), strconv.Itoa(p.MaximumAttempts), strconv.Itoa(p.MaximumConcurrentRequests)}
	if e.auth == "env" {
		values[fieldCredential] = p.APIKeyEnv
	}
	if e.auth == "file" {
		values[fieldCredential] = p.APIKeyFile
	}
	for i := range e.fields {
		input := textinput.New()
		input.Prompt = ""
		input.CharLimit = 2048
		if i == fieldCredential {
			input.CharLimit = 16384
		}
		input.SetValue(values[i])
		e.fields[i] = input
	}
	e.updateCredentialView()
	e.focusField(fieldName)
	m.editor = e
	m.screen = editorScreen
	m.notice = "Edit a draft. Apply, then save the configuration. Test uses AI tokens (up to 60s)."
	m.resize()
}

func (e *profileEditor) updateCredentialView() {
	input := &e.fields[fieldCredential]
	input.EchoMode = textinput.EchoNormal
	input.Placeholder = ""
	switch e.auth {
	case "token":
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '*'
		input.Placeholder = "Paste API token"
		if e.original.APIKey != "" {
			input.Placeholder = "Token configured; blank keeps it"
		}
	case "env":
		input.Placeholder = "Environment variable name"
	case "file":
		input.Placeholder = "Private token file path"
	case "none":
		input.Placeholder = "No authorization"
	}
}

func (m *model) draftProfile(forModels bool) (string, config.Profile, error) {
	e := m.editor
	if e == nil {
		return "", config.Profile{}, errors.New("no profile is being edited")
	}
	name := strings.TrimSpace(e.fields[fieldName].Value())
	if e.name != "" {
		name = e.name
	} else {
		if name == "" || len(name) > 64 || strings.IndexFunc(name, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_.", r)
		}) >= 0 {
			return "", config.Profile{}, errors.New("profile name must use letters, digits, dash, underscore or dot")
		}
		if _, ok := m.cfg.Profiles[name]; ok {
			return "", config.Profile{}, errors.New("profile name already exists")
		}
	}
	p := e.original
	p.BaseURL = strings.TrimSpace(e.fields[fieldURL].Value())
	p.Model = strings.TrimSpace(e.fields[fieldModel].Value())
	if forModels && p.Model == "" {
		p.Model = "model-discovery"
	}
	p.ResponseFormat, p.OutputTokenParameter, p.Stream = e.responseFormat, e.outputParameter, e.stream
	for _, field := range []struct {
		index  int
		target *int
		label  string
	}{
		{fieldContext, &p.ContextLength, "context length"}, {fieldOutput, &p.MaximumOutputTokens, "maximum output"},
		{fieldTimeout, &p.TimeoutSeconds, "timeout"}, {fieldAttempts, &p.MaximumAttempts, "attempts"}, {fieldConcurrency, &p.MaximumConcurrentRequests, "concurrency"},
	} {
		n, err := strconv.Atoi(strings.TrimSpace(e.fields[field.index].Value()))
		if err != nil {
			return "", config.Profile{}, fmt.Errorf("%s must be an integer", field.label)
		}
		*field.target = n
	}
	credential := strings.TrimSpace(e.fields[fieldCredential].Value())
	if strings.IndexFunc(credential, unicode.IsControl) >= 0 {
		return "", config.Profile{}, errors.New("credential must be one line without control characters")
	}
	endpointChanged := strings.TrimRight(p.BaseURL, "/") != strings.TrimRight(e.original.BaseURL, "/")
	p.APIKey, p.APIKeyEnv, p.APIKeyFile = "", "", ""
	switch e.auth {
	case "token":
		if credential == "" && !endpointChanged {
			credential = e.original.APIKey
		}
		if credential == "" {
			return "", config.Profile{}, errors.New("paste an API token or explicitly select another authorization source")
		}
		p.APIKey = credential
	case "env", "file":
		if credential == "" {
			return "", config.Profile{}, errors.New("enter the credential source for this endpoint")
		}
		if endpointChanged && credential == e.original.APIKeyEnv && e.auth == "env" || endpointChanged && credential == e.original.APIKeyFile && e.auth == "file" {
			if !e.changed {
				return "", config.Profile{}, errors.New("select the credential source again for the new endpoint")
			}
		}
		if e.auth == "env" {
			p.APIKeyEnv = credential
		} else {
			p.APIKeyFile = credential
		}
	case "none":
	default:
		return "", config.Profile{}, errors.New("unsupported authorization source")
	}
	if _, err := config.ResolveProfile(config.Config{Profiles: map[string]config.Profile{name: p}}, name); err != nil {
		return "", config.Profile{}, err
	}
	return name, p, nil
}

func (m *model) applyEditor() error {
	name, p, err := m.draftProfile(false)
	if err != nil {
		return err
	}
	previous, exists := m.cfg.Profiles[name]
	if !exists || !reflect.DeepEqual(previous, p) {
		m.cancelNetwork()
		m.cfg.Profiles[name] = p
		m.dirty = true
		if m.statuses[name].ProfileHash != profileHash(m.cfg, name) {
			m.statuses[name] = connectionStatus{State: "Not tested"}
		}
	}
	if m.cfg.Routing.Primary == "" {
		m.cfg.Routing.Primary = name
		m.dirty = true
	}
	m.editor = nil
	m.screen = providersScreen
	m.notice = "Profile applied to draft. Press s to save the configuration."
	return nil
}
