package tui

import (
	"strings"
	"unicode"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/ai"
)

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.resize()
		return m, nil
	case networkResult:
		m.receiveNetwork(msg)
		return m, nil
	case spinner.TickMsg:
		if m.busy {
			var command tea.Cmd
			m.spin, command = m.spin.Update(msg)
			return m, command
		}
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			m.interrupted = true
			m.cancelNetwork()
			return m, tea.Quit
		}
		if key == "esc" && m.busy {
			m.cancelNetwork()
			m.notice = "Request canceled."
			return m, nil
		}
		switch m.screen {
		case homeScreen:
			switch key {
			case "up", "k":
				m.selected = (m.selected + 4) % 5
			case "down", "j":
				m.selected = (m.selected + 1) % 5
			case "q":
				return m, m.requestExit()
			case "r":
				m.requestReload()
			case "?":
				m.showHelp()
			case "enter":
				switch m.selected {
				case 0:
					m.screen = providersScreen
				case 1:
					m.showConfiguration()
				case 2:
					m.showPrompt()
				case 3:
					m.showHelp()
				case 4:
					return m, m.requestExit()
				}
			}
		case providersScreen:
			names := m.names()
			m.profileSelected = max(0, min(m.profileSelected, len(names)-1))
			name := ""
			if len(names) > 0 {
				name = names[m.profileSelected]
			}
			switch key {
			case "esc":
				m.screen = homeScreen
			case "q":
				return m, m.requestExit()
			case "?":
				m.showHelp()
			case "up", "k":
				if len(names) > 0 {
					m.profileSelected = (m.profileSelected + len(names) - 1) % len(names)
				}
			case "down", "j":
				if len(names) > 0 {
					m.profileSelected = (m.profileSelected + 1) % len(names)
				}
			case "n":
				m.startEditor("")
			case "enter", "e":
				if name != "" {
					m.startEditor(name)
				}
			case "p":
				if name != "" {
					if m.cfg.Routing.Fallback == name {
						m.notice = "Choose a different fallback or disable it before assigning this primary."
					} else {
						m.cfg.Routing.Primary = name
						m.dirty = true
						m.notice = "Primary selected in draft. Press s to save."
					}
				}
			case "f":
				if name != "" {
					if m.cfg.Routing.Primary == name {
						m.notice = "Primary and fallback must be different profiles."
					} else {
						m.cfg.Routing.Fallback = name
						m.dirty = true
						m.notice = "Fallback selected in draft. Press s to save."
					}
				}
			case "d":
				m.cfg.Routing.Fallback = ""
				m.dirty = true
				m.notice = "Fallback disabled in draft. Press s to save."
			case "t":
				if name != "" {
					return m, m.startNetwork("probe", name)
				}
			case "s":
				m.save()
			case "r":
				m.requestReload()
			}
		case editorScreen:
			return m, m.updateEditor(message, key)
		case pickerScreen:
			choices := m.filteredChoices()
			switch key {
			case "esc":
				m.screen = editorScreen
			case "up":
				if len(choices) > 0 {
					m.pickerSelected = (m.pickerSelected + len(choices) - 1) % len(choices)
				}
			case "down":
				if len(choices) > 0 {
					m.pickerSelected = (m.pickerSelected + 1) % len(choices)
				}
			case "enter":
				if len(choices) > 0 {
					m.choose(choices[min(m.pickerSelected, len(choices)-1)])
				}
			default:
				var command tea.Cmd
				m.query, command = m.query.Update(message)
				m.pickerSelected = 0
				return m, command
			}
		case viewerScreen:
			switch key {
			case "home":
				m.viewer.GotoTop()
			case "end":
				m.viewer.GotoBottom()
			case "esc":
				m.screen = m.returnScreen
			case "q":
				return m, m.requestExit()
			default:
				var command tea.Cmd
				m.viewer, command = m.viewer.Update(message)
				return m, command
			}
		case confirmScreen:
			switch key {
			case "esc":
				m.screen = m.confirmReturn
			case "up", "k":
				m.pickerSelected = (m.pickerSelected + 2) % 3
			case "down", "j", "tab":
				m.pickerSelected = (m.pickerSelected + 1) % 3
			case "enter":
				return m, m.confirm(m.pickerSelected)
			}
		}
	case tea.PasteMsg:
		if m.screen == editorScreen {
			return m, m.updateEditor(message, "")
		}
		if m.screen == pickerScreen {
			var command tea.Cmd
			m.query, command = m.query.Update(message)
			m.pickerSelected = 0
			return m, command
		}
	default:
		if m.screen == editorScreen {
			return m, m.updateEditor(message, "")
		}
	}
	return m, nil
}

func (m *model) updateEditor(message tea.Msg, key string) tea.Cmd {
	e := m.editor
	rows := e.rows()
	switch key {
	case "esc":
		m.leaveEditor()
		return nil
	case "tab", "down":
		e.focus = (e.focus + 1) % len(rows)
		e.syncFocus()
		return nil
	case "shift+tab", "up":
		e.focus = (e.focus + len(rows) - 1) % len(rows)
		e.syncFocus()
		return nil
	}
	row := rows[e.focus]
	if row.field >= 0 {
		if key == "enter" {
			e.focus = (e.focus + 1) % len(rows)
			e.syncFocus()
			return nil
		}
		if paste, ok := message.(tea.PasteMsg); ok && row.field == fieldCredential {
			paste.Content = strings.TrimSpace(paste.Content)
			if strings.IndexFunc(paste.Content, unicode.IsControl) >= 0 {
				m.notice = "Credential paste must contain one line without control characters."
				return nil
			}
			message = paste
		}
		before := e.fields[row.field].Value()
		var command tea.Cmd
		e.fields[row.field], command = e.fields[row.field].Update(message)
		if e.fields[row.field].Value() != before {
			e.changed = true
			m.invalidate(e.fields[fieldName].Value())
			if row.field == fieldURL {
				e.fields[fieldCredential].SetValue("")
				e.fields[fieldCredential].Placeholder = "Enter credentials for the changed endpoint"
			}
		}
		return command
	}
	if key == "q" {
		return m.requestExit()
	}
	if key == "?" {
		m.showHelp()
		return nil
	}
	if key != "enter" {
		return nil
	}
	switch row.action {
	case "preset":
		choices := []string{}
		for _, p := range ai.Presets() {
			choices = append(choices, p.Name)
		}
		m.openPicker("preset", choices)
	case "auth":
		m.openPicker("auth", []string{"API token", "Environment variable", "Private token file", "No authorization"})
	case "models":
		return m.startNetwork("models", "")
	case "probe":
		return m.startNetwork("probe", "")
	case "advanced":
		e.advanced = !e.advanced
		e.syncFocus()
	case "format":
		m.openPicker("format", []string{"json_schema", "json_object"})
	case "parameter":
		m.openPicker("parameter", []string{"max_tokens", "max_completion_tokens"})
	case "stream":
		e.stream = !e.stream
		e.changed = true
		m.invalidate(e.fields[fieldName].Value())
	case "apply":
		if err := m.applyEditor(); err != nil {
			m.notice = "Profile: " + safeText(err.Error())
		}
	case "cancel":
		m.leaveEditor()
	}
	return nil
}

func (m *model) openPicker(kind string, choices []string) {
	m.pickerKind, m.choices, m.pickerSelected = kind, choices, 0
	m.query.SetValue("")
	m.query.Focus()
	m.screen = pickerScreen
}

func (m *model) filteredChoices() []string {
	result := []string{}
	query := strings.ToLower(m.query.Value())
	for i, choice := range m.choices {
		if strings.Contains(strings.ToLower(choice), query) || m.pickerKind == "model" && i == 0 {
			result = append(result, choice)
		}
	}
	return result
}

func (m *model) choose(choice string) {
	e := m.editor
	switch m.pickerKind {
	case "preset":
		for _, p := range ai.Presets() {
			if p.Name == choice {
				e.preset = choice
				e.fields[fieldURL].SetValue(p.Profile.BaseURL)
				e.fields[fieldModel].SetValue("")
				e.fields[fieldCredential].SetValue("")
				e.original.APIKey, e.original.APIKeyEnv, e.original.APIKeyFile = "", "", ""
				e.auth = "token"
				if p.Local {
					e.auth = "none"
				}
				e.responseFormat = p.Profile.ResponseFormat
				e.updateCredentialView()
			}
		}
	case "auth":
		e.auth = map[string]string{"API token": "token", "Environment variable": "env", "Private token file": "file", "No authorization": "none"}[choice]
		e.fields[fieldCredential].SetValue("")
		e.original.APIKey, e.original.APIKeyEnv, e.original.APIKeyFile = "", "", ""
		e.updateCredentialView()
	case "model":
		if choice != "Enter model ID manually" {
			e.fields[fieldModel].SetValue(choice)
		}
		e.focusField(fieldModel)
	case "format":
		e.responseFormat = choice
	case "parameter":
		e.outputParameter = choice
	}
	e.changed = true
	m.invalidate(e.fields[fieldName].Value())
	m.screen = editorScreen
}

func (m *model) requestExit() tea.Cmd {
	if !m.dirty && (m.editor == nil || !m.editor.changed) {
		m.cancelNetwork()
		return tea.Quit
	}
	m.openConfirm("exit")
	return nil
}

func (m *model) requestReload() {
	if m.dirty {
		m.openConfirm("reload")
		return
	}
	m.reload()
}

func (m *model) leaveEditor() {
	m.cancelNetwork()
	if m.editor.changed {
		m.openConfirm("editor")
		return
	}
	m.editor = nil
	m.screen = providersScreen
}

func (m *model) openConfirm(kind string) {
	m.cancelNetwork()
	m.confirmReturn = m.screen
	m.screen = confirmScreen
	m.confirmKind = kind
	m.pickerSelected = 0
}

func (m *model) confirm(selected int) tea.Cmd {
	if selected == 2 {
		m.screen = m.confirmReturn
		return nil
	}
	switch m.confirmKind {
	case "exit":
		if selected == 0 {
			if m.editor != nil && m.editor.changed {
				if err := m.applyEditor(); err != nil {
					m.screen = editorScreen
					m.notice = safeText(err.Error())
					return nil
				}
			}
			if !m.save() {
				m.screen = providersScreen
				return nil
			}
		}
		m.cancelNetwork()
		return tea.Quit
	case "editor":
		if selected == 0 {
			if err := m.applyEditor(); err != nil {
				m.screen = editorScreen
				m.notice = safeText(err.Error())
			}
		} else {
			m.invalidate(m.editor.fields[fieldName].Value())
			m.editor = nil
			m.screen = providersScreen
		}
	case "reload":
		if selected == 0 {
			if !m.save() {
				m.screen = m.confirmReturn
				return nil
			}
		}
		m.reload()
		m.screen = providersScreen
	}
	return nil
}
