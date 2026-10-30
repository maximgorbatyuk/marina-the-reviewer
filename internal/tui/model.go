package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/ai"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

type screen int

const (
	homeScreen screen = iota
	providersScreen
	editorScreen
	pickerScreen
	viewerScreen
	confirmScreen
)

type connectionStatus struct {
	State, Message string
	ProfileHash    string
	At             time.Time
	Elapsed        time.Duration
}

type networkResult struct {
	ID         uint64
	Kind, Name string
	Hash       string
	Models     []string
	Elapsed    time.Duration
	Err        error
}

type model struct {
	ctx                                      context.Context
	path, revision                           string
	cfg                                      config.Config
	dirty, loadFailed, interrupted           bool
	screen, returnScreen                     screen
	confirmReturn                            screen
	width, height, selected, profileSelected int
	notice, viewerTitle                      string
	viewer                                   viewport.Model
	spin                                     spinner.Model
	editor                                   *profileEditor
	pickerKind                               string
	choices                                  []string
	query                                    textinput.Model
	pickerSelected                           int
	confirmKind                              string
	statuses                                 map[string]connectionStatus
	busy                                     bool
	operationID                              uint64
	operationProfile                         string
	operationCancel                          context.CancelFunc
}

func newModel(ctx context.Context, path string) *model {
	m := &model{ctx: ctx, path: path, width: 80, height: 24, viewer: viewport.New(), spin: spinner.New(), query: textinput.New()}
	m.spin.Spinner = spinner.Dot
	m.query.Prompt = "Search: "
	m.query.CharLimit = 256
	m.viewer.SoftWrap = true
	m.reload()
	m.resize()
	return m
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) reload() {
	m.cancelNetwork()
	cfg, revision, err := config.OpenForEdit(m.path)
	m.loadFailed = err != nil
	if err != nil {
		m.cfg = config.Config{Version: 1, Profiles: map[string]config.Profile{}}
		m.notice = "Cannot load configuration: " + safeText(err.Error()) + ". Fix the file, then press r to reload."
	} else {
		m.cfg, m.revision = cfg, revision
		m.notice = ""
	}
	m.statuses = map[string]connectionStatus{}
	m.dirty = false
}

func (m *model) names() []string {
	names := make([]string, 0, len(m.cfg.Profiles))
	for name := range m.cfg.Profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (m *model) save() bool {
	if m.loadFailed {
		m.notice = "Fix and reload the existing configuration before saving."
		return false
	}
	revision, err := config.Save(m.path, m.cfg, m.revision)
	if err != nil {
		m.notice = "Save failed: " + safeText(err.Error())
		return false
	}
	m.revision, m.dirty = revision, false
	m.notice = "Configuration saved."
	return true
}

func (m *model) invalidate(name string) {
	m.cancelNetwork()
	m.statuses[name] = connectionStatus{State: "Not tested"}
}

func (m *model) cancelNetwork() {
	if m.operationCancel != nil {
		m.operationCancel()
		m.operationCancel = nil
	}
	if m.busy {
		m.statuses[m.operationProfile] = connectionStatus{State: "Canceled", At: time.Now()}
	}
	m.busy = false
	m.operationID++
}

func (m *model) startNetwork(kind, name string) tea.Cmd {
	if m.loadFailed {
		m.notice = "Fix and reload the configuration first."
		return nil
	}
	cfg := m.cfg
	if name == "" {
		var profile config.Profile
		var err error
		name, profile, err = m.draftProfile(kind == "models")
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		cfg = config.Config{Profiles: map[string]config.Profile{name: profile}}
	}
	provider, err := config.ResolveProfile(cfg, name)
	if err != nil {
		m.notice = "Profile is invalid: " + safeText(err.Error())
		return nil
	}
	token, err := config.ResolveToken(provider, cfg, nil)
	if err != nil {
		m.notice = "Credentials unavailable: " + safeText(err.Error())
		return nil
	}
	m.cancelNetwork()
	ctx, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.busy = true
	m.operationProfile = name
	m.statuses[name] = connectionStatus{State: "Checking"}
	id := m.operationID
	hash := profileHash(cfg, name)
	m.notice = "Checking selected profile; Esc cancels."
	command := func() tea.Msg {
		started := time.Now()
		result := networkResult{ID: id, Kind: kind, Name: name, Hash: hash}
		if kind == "models" {
			result.Models, result.Err = ai.ListModels(ctx, provider, token)
		} else {
			_, result.Err = ai.Probe(ctx, provider, token)
		}
		result.Elapsed = time.Since(started)
		return result
	}
	return tea.Batch(command, m.spin.Tick)
}

func (m *model) receiveNetwork(result networkResult) {
	if result.ID != m.operationID {
		return
	}
	m.busy = false
	if m.operationCancel != nil {
		m.operationCancel()
		m.operationCancel = nil
	}
	if result.Kind == "models" {
		m.statuses[result.Name] = connectionStatus{State: "Not tested"}
		if m.editor == nil || m.screen != editorScreen {
			return
		}
		if result.Err != nil {
			m.notice = "Models: " + safeText(result.Err.Error()) + ". A manual model ID is always available."
			return
		}
		m.openPicker("model", append([]string{"Enter model ID manually"}, result.Models...))
		m.notice = "Select a model or return to manual entry. Model discovery does not verify inference access."
		return
	}
	status := connectionStatus{State: "Verified", At: time.Now(), Elapsed: result.Elapsed, ProfileHash: result.Hash}
	if result.Err != nil {
		status.State = "Failed"
		status.Message = safeText(result.Err.Error())
		if errors.Is(result.Err, context.Canceled) {
			status.State = "Canceled"
		}
		if errors.Is(result.Err, context.DeadlineExceeded) {
			status.Message = "timeout: connection test exceeded its deadline"
		}
	}
	m.statuses[result.Name] = status
	if result.Err != nil {
		m.notice = status.State + ": " + status.Message
	} else {
		m.notice = "Verified: selected model returned valid JSON. Full code review was not run."
	}
}

func (m *model) status(name string) string {
	s, ok := m.statuses[name]
	if !ok || s.State == "" {
		return "Not tested"
	}
	if s.State == "Verified" && s.ProfileHash != profileHash(m.cfg, name) {
		return "Not tested"
	}
	if s.State == "Checking" {
		return m.spin.View() + " Checking"
	}
	if s.At.IsZero() {
		return s.State
	}
	return fmt.Sprintf("%s at %s (%s)", s.State, s.At.Format("15:04:05"), s.Elapsed.Round(time.Millisecond))
}

func profileHash(cfg config.Config, name string) string {
	snapshot, err := config.ResolveProfile(cfg, name)
	if err != nil {
		return ""
	}
	p := cfg.Profiles[name]
	value := struct {
		Profile        config.Profile
		Key, Env, File string
	}{snapshot.Profile, p.APIKey, p.APIKeyEnv, p.APIKeyFile}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (m *model) resize() {
	m.viewer.SetWidth(max(10, m.width-4))
	m.viewer.SetHeight(max(1, m.height-8))
	m.query.SetWidth(max(8, m.width-16))
	if m.editor != nil {
		for i := range m.editor.fields {
			m.editor.fields[i].SetWidth(max(8, m.width-30))
		}
	}
}
