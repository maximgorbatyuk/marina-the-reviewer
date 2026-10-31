package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/buildinfo"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

const logo = ` __  __    _    ____  ___ _   _    _
|  \/  |  / \  |  _ \|_ _| \ | |  / \
| |\/| | / _ \ | |_) || ||  \| | / _ \
| |  | |/ ___ \|  _ < | || |\  |/ ___ \
|_|  |_/_/   \_\_| \_\___|_| \_/_/   \_\`

var (
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	muted  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

var homeMenu = []string{"Providers and routing", "Configuration and connection status", "Usage", "Default review prompt", "Help", "Exit"}

func safeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
}

func (m *model) heading(full bool) []string {
	version := "marinadoreview v" + strings.TrimPrefix(buildinfo.Version, "v")
	if full && m.width >= 75 && m.height >= 24 {
		lines := strings.Split(cyan.Render(logo), "\n")
		return append(lines, cyan.Render(version), "https://github.com/maximgorbatyuk/marina-the-reviewer", muted.Render("(c) maximgorbatyuk"), "")
	}
	return []string{cyan.Render(version) + "  " + muted.Render("(c) maximgorbatyuk"), ""}
}

func selection(text string, selected bool) string {
	if selected {
		return yellow.Render("> " + text)
	}
	return "  " + text
}

func (m *model) View() tea.View {
	lines := m.heading(m.screen == homeScreen)
	foot := ""
	focus := -1
	var body []string
	switch m.screen {
	case homeScreen:
		mark := ""
		if m.dirty {
			mark = " [unsaved]"
		}
		lines = append(lines, "Config: "+safeText(m.path)+mark, m.roleLine("Primary", m.cfg.Routing.Primary), m.roleLine("Fallback", m.cfg.Routing.Fallback), "")
		for i, item := range homeMenu {
			body = append(body, selection(item, m.selected == i))
		}
		focus = m.selected
		foot = "↑/k ↓/j navigate  Enter select  ? help  r reload  q quit"
	case providersScreen:
		lines = append(lines, cyan.Render("Providers and routing"), "Enter edit  n new  p primary  f fallback  d disable fallback", "")
		names := m.names()
		if len(names) == 0 {
			body = []string{"No profiles. Press n to create your primary profile."}
		}
		for i, name := range names {
			p := m.cfg.Profiles[name]
			role := ""
			if name == m.cfg.Routing.Primary {
				role = " [primary]"
			}
			if name == m.cfg.Routing.Fallback {
				role = " [fallback]"
			}
			body = append(body, selection(safeText(name)+role+"  "+safeText(p.Model), m.profileSelected == i), "    "+safeText(p.BaseURL)+"  | "+m.status(name))
		}
		focus = m.profileSelected * 2
		foot = "t test  s save  r reload  ? help  Esc back  q quit"
	case editorScreen:
		lines = append(lines, cyan.Render("Edit provider profile"), "Apply edits to draft, then save from Providers (s).", "")
		e := m.editor
		for i, row := range e.rows() {
			label := row.label
			value := ""
			if row.field >= 0 {
				value = e.fields[row.field].View()
			} else {
				switch row.action {
				case "":
					if row.label == "Credential" {
						value = "Not required"
					} else {
						value = safeText(e.name) + " (fixed)"
					}
				case "preset":
					value = e.preset
				case "auth":
					value = map[string]string{"token": "API token", "env": "Environment variable", "file": "Private token file", "none": "No authorization"}[e.auth]
				case "format":
					value = e.responseFormat
				case "parameter":
					value = e.outputParameter
				case "stream":
					value = fmt.Sprint(e.stream)
				case "advanced":
					if e.advanced {
						value = "Hide"
					} else {
						value = "Show"
					}
				}
			}
			text := label
			if value != "" {
				text += "  " + value
			}
			body = append(body, selection(text, e.focus == i))
		}
		focus = e.focus
		foot = "Tab/Shift+Tab move  Enter select  Esc back/cancel request"
	case pickerScreen:
		lines = append(lines, cyan.Render("Select "+m.pickerKind), m.query.View(), "")
		for i, choice := range m.filteredChoices() {
			body = append(body, selection(safeText(choice), m.pickerSelected == i))
		}
		if len(body) == 0 {
			body = []string{"No matches. Esc returns to manual model entry."}
		}
		focus = m.pickerSelected
		foot = "Type to filter  ↑/↓ select  Enter apply  Esc back"
	case usageScreen:
		lines = append(lines, cyan.Render("Usage — tokens and code reviews"), m.usagePeriods(), "")
		body = strings.Split(m.usage.viewport.View(), "\n")
		foot = "←/→ period  ↑/↓ scroll  r refresh  Esc back  ? help"
	case viewerScreen:
		lines = append(lines, cyan.Render(m.viewerTitle), "")
		body = strings.Split(m.viewer.View(), "\n")
		foot = "↑/↓ PgUp/PgDn Home/End scroll  Esc back  q quit"
	case confirmScreen:
		lines = append(lines, cyan.Render("Unsaved changes"), "")
		choices := []string{"Save and exit", "Discard and exit", "Cancel"}
		if m.confirmKind == "editor" {
			choices = []string{"Apply to draft", "Discard profile edits", "Cancel"}
		}
		if m.confirmKind == "reload" {
			choices = []string{"Save and reload", "Discard and reload", "Cancel"}
		}
		for i, choice := range choices {
			body = append(body, selection(choice, m.pickerSelected == i))
		}
		focus = m.pickerSelected
		foot = "↑/↓ select  Enter confirm  Esc cancel"
	}
	if m.busy {
		foot = m.spin.View() + " Request in progress; Esc cancels. " + foot
	}
	notice := safeText(m.notice)
	if m.screen == usageScreen {
		notice = ""
	}
	if m.loadFailed && m.screen != usageScreen {
		notice = "Config unavailable. " + notice
	}
	available := max(1, m.height-len(lines)-3)
	start := 0
	if focus >= available {
		start = focus - available + 1
	}
	start = min(start, max(0, len(body)-available))
	end := min(len(body), start+available)
	if start < len(body) {
		lines = append(lines, body[start:end]...)
	}
	for len(lines) < m.height-3 {
		lines = append(lines, "")
	}
	lines = append(lines, "", notice, muted.Render(foot))
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(1, m.width-1), "…")
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

func (m *model) roleLine(role, name string) string {
	if name == "" {
		return role + ": Disabled"
	}
	return role + ": " + safeText(name) + "  " + safeText(m.cfg.Profiles[name].Model) + "  | " + m.status(name)
}

func (m *model) showViewer(title, content string) {
	m.cancelNetwork()
	m.returnScreen = m.screen
	m.screen = viewerScreen
	m.viewerTitle = title
	m.viewer.SetContent(content)
	m.viewer.GotoTop()
}

func (m *model) showPrompt() { m.showViewer("Default review prompt", review.DefaultSystemPrompt()) }

func (m *model) showConfiguration() {
	var b strings.Builder
	fmt.Fprintf(&b, "Config: %s\nSchema version: %d\nPrimary: %s\nFallback: %s\nFallback on: %s\n", safeText(m.path), m.cfg.Version, safeText(m.cfg.Routing.Primary), safeText(m.cfg.Routing.Fallback), strings.Join(m.cfg.Routing.FallbackOn, ", "))
	for _, name := range m.names() {
		p := m.cfg.Profiles[name]
		if snapshot, err := config.ResolveProfile(m.cfg, name); err == nil {
			p = snapshot.Profile
		}
		auth := authSource(m.cfg.Profiles[name])
		source := "Not configured"
		switch auth {
		case "token":
			source = "Token configured (hidden)"
		case "env":
			source = "Environment: " + safeText(m.cfg.Profiles[name].APIKeyEnv)
		case "file":
			source = "Private file: " + safeText(m.cfg.Profiles[name].APIKeyFile)
		}
		fmt.Fprintf(&b, "\n%s\n  Backend: %s\n  API URL: %s\n  Model: %s\n  Authorization: %s\n  Response format: %s\n  Output parameter: %s\n  Context/output: %d/%d\n  Timeout/attempts/concurrency: %d/%d/%d\n  Stream: %t\n  Extra parameters: %d (values hidden)\n  Connection: %s\n", safeText(name), safeText(p.Backend), safeText(p.BaseURL), safeText(p.Model), source, p.ResponseFormat, p.OutputTokenParameter, p.ContextLength, p.MaximumOutputTokens, p.TimeoutSeconds, p.MaximumAttempts, p.MaximumConcurrentRequests, p.Stream, len(p.ExtraParameters), m.status(name))
		if s := m.statuses[name]; s.Message != "" {
			fmt.Fprintf(&b, "  Last error: %s\n", s.Message)
		}
	}
	m.showViewer("Configuration and connection status", b.String())
}

func (m *model) showHelp() {
	m.showViewer("Help", `marinadoreview — AI code and architecture review

Start
  marinadoreview                 Open the TUI (terminal required)
  marinadoreview --config PATH   Open a selected config
  marinadoreview --help          Print CLI help without a terminal
  marinadoreview --version       Print the build version

Providers and routing
  n creates a profile; Enter edits the selected profile.
  p selects primary; f selects fallback; d disables fallback.
  Several profiles can be saved; only the chosen fallback is tried.
  s saves the draft; r reloads the file.
  Authentication and invalid output do not trigger fallback.

Models and credentials
  Choose a provider preset or enter a custom compatible API URL.
  Paste a token, refer to an environment variable/private file,
  or explicitly choose no authorization for a suitable local server.
  A blank replacement keeps the stored token. Selecting an auth
  source again clears it explicitly. Changed endpoints require
  credentials to be entered again.
  Load models needs API access. A manual model ID always works.
  Model lists do not prove inference access or review compatibility.
  Advanced settings control JSON mode, context/output limits,
  output token parameter, timeout, retries, concurrency and stream.

Connection test
  t tests the selected saved/draft profile; the editor has a button.
  The test sends one small AI request and can use billed tokens.
  It checks the selected model and JSON mode with the configured request timeout.
  It does not send source code, retry or switch to fallback.
  Verified is the last test result, not a persistent connection.
  Parameter changes reset the status. Esc cancels a running request.

Usage screen
  Open Usage from the home menu. 1/2/3 select 7/30/90 days.
  Left/Right or Tab changes the period. r refreshes local data.
  Arrows, PgUp/PgDn and Home/End scroll. Esc returns home.
  Tokens and code review counts match the usage command.
  SQLite keeps 90 days. Missing usage is shown as ?.
  Unknown or partial requests make token totals incomplete.
  This screen does not send AI requests. It can import old history
  and remove expired statistics from the local database.

Review commands
  prepare   Read pinned Git objects and prepare source context
  plan      Build a review plan using saved provider settings
  run       Execute a prepared plan
  review    Prepare, plan and run a review
  validate  Validate model output against source
  compat    Import/export Marina-compatible JSON
  usage     Show locally recorded tokens for 7, 30 and 90 days
            usage --period 7d [--json] selects one window
  prompts show --builtin review

Prompt override
  --system-prompt TEXT or --system-prompt-file PATH overrides policy.
  The technical JSON protocol stays attached for validation.

Navigation
  Menus: arrows or j/k, Enter. Forms: Tab/Shift+Tab, Enter, Esc.
  Input fields accept q/j/k/? as text; terminal paste is supported.
  Viewers: arrows, PgUp/PgDn, Home/End. Esc returns; q exits.
  Ctrl+C cancels the interface. Unsaved changes are not written.

https://github.com/maximgorbatyuk/marina-the-reviewer
(c) maximgorbatyuk`)
}
