package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

func testModel(t *testing.T) *model {
	t.Helper()
	m := newModel(context.Background(), filepath.Join(t.TempDir(), "config.json"))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

func key(code rune, text string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Text: text} }

func TestHomeNavigationAndResize(t *testing.T) {
	m := testModel(t)
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 18}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View().Content
		if !strings.Contains(view, "maximgorbatyuk") || !strings.Contains(view, "marinadoreview") {
			t.Fatal("missing identity")
		}
		if len(strings.Split(view, "\n")) > size[1] {
			t.Fatal("view exceeds terminal height")
		}
	}
	m.Update(key(tea.KeyEnter, ""))
	if m.screen != providersScreen {
		t.Fatal("menu did not open providers")
	}
	m.Update(key('n', "n"))
	if m.screen != editorScreen {
		t.Fatal("new profile did not open")
	}
}

func TestDraftPreservesProfilesAndSavesV1(t *testing.T) {
	m := testModel(t)
	m.cfg.Profiles["unused"] = config.Profile{BaseURL: "http://localhost:1234/v1", Model: "unused", ExtraParameters: map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"low"`)}}
	m.startEditor("")
	m.editor.fields[fieldName].SetValue("cloud")
	m.editor.fields[fieldURL].SetValue("http://localhost:1234/v1")
	m.editor.fields[fieldModel].SetValue("manual-model")
	m.editor.auth = "none"
	if err := m.applyEditor(); err != nil {
		t.Fatal(err)
	}
	if m.cfg.Routing.Primary != "cloud" || !m.dirty {
		t.Fatal("first profile not assigned")
	}
	m.cfg.Routing.Fallback = "unused"
	if !m.save() {
		t.Fatal(m.notice)
	}
	loaded, err := config.Load(m.path)
	if err != nil || loaded.Version != 1 || loaded.Profiles["cloud"].Model != "manual-model" || string(loaded.Profiles["unused"].ExtraParameters["reasoning_effort"]) != `"low"` {
		t.Fatal("saved draft lost settings")
	}
	if strings.Contains(string(mustRead(t, m.path)), "Verified") {
		t.Fatal("status persisted")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestTokenIsMaskedAndEmptyReplacementPreservesIt(t *testing.T) {
	m := testModel(t)
	m.cfg.Profiles["cloud"] = config.Profile{BaseURL: "https://provider.example/v1", Model: "chosen", APIKey: "synthetic-private-value"}
	m.cfg.Routing.Primary = "cloud"
	m.startEditor("cloud")
	if m.editor.fields[fieldCredential].Value() != "" {
		t.Fatal("saved token copied into input")
	}
	m.editor.fields[fieldModel].SetValue("new-model")
	if err := m.applyEditor(); err != nil {
		t.Fatal(err)
	}
	if m.cfg.Profiles["cloud"].APIKey != "synthetic-private-value" {
		t.Fatal("empty replacement deleted token")
	}
	m.startEditor("cloud")
	m.editor.focusField(fieldCredential)
	m.Update(tea.PasteMsg{Content: "pasted-qjk?-value"})
	if m.screen != editorScreen || m.editor.fields[fieldCredential].Value() != "pasted-qjk?-value" {
		t.Fatal("paste invoked navigation")
	}
	if strings.Contains(m.View().Content, "pasted-qjk?-value") {
		t.Fatal("input exposed token")
	}
	if err := m.applyEditor(); err != nil {
		t.Fatal(err)
	}
	if m.cfg.Profiles["cloud"].APIKey != "pasted-qjk?-value" {
		t.Fatal("paste not applied")
	}
	m.showConfiguration()
	if strings.Contains(m.viewer.GetContent(), "pasted-qjk?-value") {
		t.Fatal("config viewer exposed token")
	}
}

func TestChangedEndpointRequiresNewCredential(t *testing.T) {
	m := testModel(t)
	m.cfg.Profiles["cloud"] = config.Profile{BaseURL: "https://old.example/v1", Model: "chosen", APIKey: "synthetic-value"}
	m.startEditor("cloud")
	m.editor.fields[fieldURL].SetValue("https://new.example/v1")
	if err := m.applyEditor(); err == nil {
		t.Fatal("credential reused at new endpoint")
	}
	m.editor.fields[fieldCredential].SetValue("new-synthetic-value")
	if err := m.applyEditor(); err != nil {
		t.Fatal(err)
	}
}

func TestPromptViewUsesBuiltInAndScrolls(t *testing.T) {
	m := testModel(t)
	m.showPrompt()
	if m.viewer.GetContent() != review.DefaultSystemPrompt() {
		t.Fatal("prompt duplicated or truncated")
	}
	m.Update(key(tea.KeyPgDown, ""))
	if m.viewer.YOffset() == 0 {
		t.Fatal("prompt does not scroll")
	}
	m.Update(key(tea.KeyHome, ""))
	if m.viewer.YOffset() != 0 {
		t.Fatal("Home did not scroll to top")
	}
	m.Update(key(tea.KeyEnd, ""))
	if !m.viewer.AtBottom() {
		t.Fatal("End did not scroll to bottom")
	}
	m.Update(key(tea.KeyEscape, ""))
	if m.screen != homeScreen {
		t.Fatal("viewer did not return")
	}
}

func TestStaleConnectionResultCannotVerifyChangedProfile(t *testing.T) {
	m := testModel(t)
	m.operationID = 2
	m.busy = true
	m.operationProfile = "cloud"
	m.statuses["cloud"] = connectionStatus{State: "Not tested"}
	m.Update(networkResult{ID: 1, Kind: "probe", Name: "cloud", Elapsed: time.Second})
	if m.statuses["cloud"].State != "Not tested" {
		t.Fatal("stale result verified current settings")
	}
	m.Update(networkResult{ID: 2, Kind: "probe", Name: "cloud", Elapsed: time.Second})
	if m.statuses["cloud"].State != "Verified" || m.busy {
		t.Fatal("current result ignored")
	}
	m.startEditor("")
	m.invalidate("cloud")
	if m.statuses["cloud"].State != "Not tested" {
		t.Fatal("edit retained verified state")
	}
}

func TestSaveConflictAndUnsavedExit(t *testing.T) {
	m := testModel(t)
	m.cfg = config.Config{Version: 1, Routing: config.Routing{Primary: "one"}, Profiles: map[string]config.Profile{"one": {BaseURL: "http://localhost:1234", Model: "one"}}}
	m.dirty = true
	m.Update(key('q', "q"))
	if m.screen != confirmScreen {
		t.Fatal("exit discarded unsaved config")
	}
	if !m.save() {
		t.Fatal(m.notice)
	}
	data := append(mustRead(t, m.path), ' ')
	if err := os.WriteFile(m.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	m.dirty = true
	if m.save() {
		t.Fatal("external change overwritten")
	}
	if string(mustRead(t, m.path)) != string(data) {
		t.Fatal("conflict modified config")
	}
}

func TestViewerBackAfterCanceledExit(t *testing.T) {
	m := testModel(t)
	m.dirty = true
	m.showPrompt()
	m.Update(key('q', "q"))
	m.Update(key(tea.KeyEscape, ""))
	m.Update(key(tea.KeyEscape, ""))
	if m.screen != homeScreen {
		t.Fatal("confirmation lost viewer navigation history")
	}
}

func TestClosedEditorIgnoresModelResponse(t *testing.T) {
	m := testModel(t)
	m.startEditor("")
	m.busy = true
	m.operationID = 4
	m.operationProfile = "profile-1"
	m.leaveEditor()
	m.Update(networkResult{ID: 4, Kind: "models", Name: "profile-1", Models: []string{"model"}})
	if m.screen != providersScreen || m.busy {
		t.Fatal("model response reopened a closed editor")
	}
}

func TestDiscardedProbeIsNotAppliedToSavedProfile(t *testing.T) {
	m := testModel(t)
	m.cfg.Profiles["cloud"] = config.Profile{BaseURL: "https://provider.example/v1", Model: "old", APIKey: "synthetic-value"}
	m.startEditor("cloud")
	m.editor.fields[fieldModel].SetValue("new")
	m.editor.changed = true
	name, p, err := m.draftProfile(false)
	if err != nil {
		t.Fatal(err)
	}
	m.statuses[name] = connectionStatus{State: "Verified", ProfileHash: profileHash(config.Config{Profiles: map[string]config.Profile{name: p}}, name)}
	if strings.Contains(m.status(name), "Verified") {
		t.Fatal("draft probe verified old saved settings")
	}
	if err := m.applyEditor(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.status(name), "Verified") {
		t.Fatal("applying tested settings discarded successful check")
	}
	m.startEditor(name)
	m.editor.fields[fieldModel].SetValue("discarded")
	m.editor.changed = true
	_, p, err = m.draftProfile(false)
	if err != nil {
		t.Fatal(err)
	}
	m.statuses[name] = connectionStatus{State: "Verified", ProfileHash: profileHash(config.Config{Profiles: map[string]config.Profile{name: p}}, name)}
	m.leaveEditor()
	m.confirm(1)
	if strings.Contains(m.status(name), "Verified") {
		t.Fatal("discarded draft probe verified saved settings")
	}
}

func TestRoutingUsesSelectedProfilesAndOptionalFallback(t *testing.T) {
	m := testModel(t)
	for _, name := range []string{"a", "b", "c"} {
		m.cfg.Profiles[name] = config.Profile{BaseURL: "http://localhost:1234/v1", Model: name}
	}
	m.cfg.Routing.Primary = "a"
	m.screen = providersScreen
	m.Update(key('f', "f"))
	if m.cfg.Routing.Fallback != "" {
		t.Fatal("primary also assigned fallback")
	}
	m.Update(key(tea.KeyDown, ""))
	m.Update(key('f', "f"))
	if m.cfg.Routing.Fallback != "b" {
		t.Fatal("selected fallback not assigned")
	}
	m.Update(key(tea.KeyDown, ""))
	m.Update(key('p', "p"))
	if m.cfg.Routing.Primary != "c" || len(m.cfg.Profiles) != 3 {
		t.Fatal("primary change lost profiles")
	}
	m.Update(key('d', "d"))
	if m.cfg.Routing.Fallback != "" {
		t.Fatal("fallback not disabled")
	}
}

func TestModelPickerPreservesManualIDAndSupportsSearch(t *testing.T) {
	m := testModel(t)
	m.startEditor("")
	m.editor.fields[fieldModel].SetValue("saved-manual")
	m.openPicker("model", []string{"Enter model ID manually", "alpha", "beta"})
	m.query.SetValue("beta")
	if choices := m.filteredChoices(); len(choices) != 2 || choices[1] != "beta" {
		t.Fatal("search lost manual entry")
	}
	m.Update(key(tea.KeyEscape, ""))
	if m.editor.fields[fieldModel].Value() != "saved-manual" {
		t.Fatal("opening list replaced manual model")
	}
}

func TestInvalidTokenPasteDoesNotEnterField(t *testing.T) {
	m := testModel(t)
	m.startEditor("")
	m.editor.focusField(fieldCredential)
	m.Update(tea.PasteMsg{Content: "line-one\nline-two"})
	if m.editor.fields[fieldCredential].Value() != "" {
		t.Fatal("multiline token silently changed")
	}
	m.Update(tea.PasteMsg{Content: " one-line\n"})
	if m.editor.fields[fieldCredential].Value() != "one-line" {
		t.Fatal("paste trailing newline not handled")
	}
}

func TestConfigLoadFailureCannotOverwriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), path)
	m.startEditor("")
	if !m.loadFailed || m.editor != nil || m.save() {
		t.Fatal("broken configuration was editable")
	}
	m.showPrompt()
	if m.screen != viewerScreen || string(mustRead(t, path)) != "broken" {
		t.Fatal("load failure prevented help or changed config")
	}
}
