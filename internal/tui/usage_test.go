package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func TestHomeOpensUsageScreen(t *testing.T) {
	m := testModel(t)
	if !strings.Contains(ansi.Strip(m.View().Content), "\n  Usage\n") {
		t.Fatal("Usage menu entry is missing")
	}
	m.Update(key(tea.KeyDown, ""))
	m.Update(key(tea.KeyDown, ""))
	m.Update(key(tea.KeyEnter, ""))
	if !strings.Contains(m.View().Content, "Period:") {
		t.Fatal("Usage did not open its period selector")
	}
}

func usageFixture(t *testing.T) (*model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
	t.Setenv("MARINADOREVIEW_DB_FILE", path)
	t.Setenv("MARINADOREVIEW_USAGE_FILE", "")
	now := time.Now().UTC()
	for i, days := range []int{1, 12, 45} {
		at := now.Add(-time.Duration(days) * 24 * time.Hour)
		input, output, total := int64(10*(i+1)), int64(i+1), int64(11*(i+1))
		e := usage.Event{Version: 1, ID: fmt.Sprint(i), Kind: "finish", At: at, StartedAt: at, ProviderID: "https://example.test/v1", Provider: "Example", Profile: "cloud", Model: "model", Operation: "review", Status: "success", Counts: usage.Counts{Input: &input, Output: &output, Total: &total}}
		if err := (usage.Store{Path: path}).Append(e); err != nil {
			t.Fatal(err)
		}
		finished := at.Add(2 * time.Second)
		if err := (usage.Store{Path: path}).SaveReview(usage.Review{RunID: fmt.Sprint("review-", i), PassID: "pass", PlanHash: "plan", StartedAt: at, FinishedAt: &finished, Status: "complete", CheckedBlocks: 2, WarningFindings: 1}); err != nil {
			t.Fatal(err)
		}
	}
	return testModel(t), path
}

func TestUsageShowsTokenAndReviewSections(t *testing.T) {
	m, _ := usageFixture(t)
	finishUsageLoad(t, m, m.showUsage())
	for _, step := range []struct {
		key     string
		reviews string
	}{{"1", "Reviews: 1"}, {"2", "Reviews: 2"}, {"3", "Reviews: 3"}} {
		m.Update(key(rune(step.key[0]), step.key))
		content := m.usage.viewport.GetContent()
		if !strings.Contains(content, "Code reviews") || !strings.Contains(content, step.reviews) || !strings.Contains(content, "All providers (known tokens)") {
			t.Fatalf("missing statistics for period %s: %s", step.key, content)
		}
	}
}

func finishUsageLoad(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("no asynchronous usage read returned")
	}
	m.Update(cmd())
	if m.usage.loading || m.usage.err != "" {
		t.Fatalf("usage read failed: %s", m.usage.err)
	}
}

func TestUsageUsesSharedReportAndSwitchesPeriods(t *testing.T) {
	m, path := usageFixture(t)
	before := mustRead(t, path)
	finishUsageLoad(t, m, m.showUsage())
	expected, err := usage.Generate(usage.Store{Path: path}, m.usage.report.GeneratedAt, []int{7, 30, 90})
	if err != nil || !reflect.DeepEqual(expected, m.usage.report) {
		t.Fatal("TUI differs from shared usage report")
	}
	for _, step := range []struct {
		key    tea.KeyPressMsg
		days   int
		tokens string
	}{
		{key('1', "1"), 7, "Total: 11"}, {key('2', "2"), 30, "Total: 33"}, {key('3', "3"), 90, "Total: 66"},
		{key(tea.KeyRight, ""), 7, "Total: 11"}, {key(tea.KeyLeft, ""), 90, "Total: 66"}, {key(tea.KeyTab, ""), 7, "Total: 11"},
	} {
		_, cmd := m.Update(step.key)
		if cmd != nil || m.usage.days != step.days || !strings.Contains(m.usage.viewport.GetContent(), step.tokens) {
			t.Fatalf("period %d did not show expected totals", step.days)
		}
	}
	if string(mustRead(t, path)) != string(before) {
		t.Fatal("usage screen wrote to journal")
	}
}

func TestUsageShowsProvidersAndIncompleteCounts(t *testing.T) {
	m, path := usageFixture(t)
	at := time.Now().UTC().Add(-time.Hour)
	input := int64(987654321012345678)
	for _, event := range []usage.Event{
		{Version: 1, ID: "partial", Kind: "finish", At: at, StartedAt: at, ProviderID: "https://partial.test/v1", Provider: "Partial provider", Profile: "partial", Model: "model", Operation: "review", Status: "failed", Counts: usage.Counts{Input: &input}},
		{Version: 1, ID: "unknown", Kind: "finish", At: at, StartedAt: at, ProviderID: "https://unknown.test/v1", Provider: "Unknown provider", Profile: "unknown", Model: "model", Operation: "review", Status: "success"},
		{Version: 1, ID: "pending", Kind: "start", At: at, StartedAt: at, ProviderID: "https://unknown.test/v1", Provider: "Unknown provider", Profile: "unknown", Model: "model", Operation: "review"},
	} {
		if err := (usage.Store{Path: path}).Append(event); err != nil {
			t.Fatal(err)
		}
	}
	before := mustRead(t, path)
	finishUsageLoad(t, m, m.showUsage())
	m.usage.report.IncompleteTail = true
	m.rebuildUsage()
	content := m.usage.viewport.GetContent()
	for _, want := range []string{
		"All providers (known tokens)", "Example", "Partial provider", "Unknown provider",
		"Input: 987654321012345678   Output: ?   Total: ?",
		"Input: ?   Output: ?   Total: ?", "Requests: 4   Failed: 1",
		"Unknown: 2   Partial: 1   Unfinished: 1", "token totals incomplete",
		"incomplete final journal entry was excluded",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("usage is missing %q", want)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	var pages strings.Builder
	for {
		view := ansi.Strip(m.View().Content)
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) >= 60 {
				t.Fatal("usage line exceeds terminal width")
			}
		}
		pages.WriteString(view)
		if m.usage.viewport.AtBottom() {
			break
		}
		m.Update(key(tea.KeyPgDown, ""))
	}
	for _, want := range []string{"987654321012345678", "Unknown provider", "incomplete final journal entry"} {
		if !strings.Contains(pages.String(), want) {
			t.Errorf("scrolling does not expose %q", want)
		}
	}
	if string(mustRead(t, path)) != string(before) {
		t.Fatal("usage screen changed the incomplete journal")
	}
}

func TestUsageEmptyErrorAndRetryWithoutProviderConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "stats.sqlite3")
	t.Setenv("MARINADOREVIEW_DB_FILE", path)
	t.Setenv("MARINADOREVIEW_USAGE_FILE", "")
	m := testModel(t)
	m.loadFailed = true
	m.notice = "provider config cannot be read"
	finishUsageLoad(t, m, m.showUsage())
	if !strings.Contains(m.usage.viewport.GetContent(), "No API requests recorded") || strings.Contains(m.View().Content, "Config unavailable") {
		t.Fatal("Usage depends on provider config")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("empty screen created usage storage")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, cmd := m.Update(key('r', "r"))
	m.Update(cmd())
	if m.usage.err == "" || !strings.Contains(m.usage.viewport.GetContent(), "Cannot load usage") {
		t.Fatal("read error was hidden")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, cmd = m.Update(key('r', "r"))
	finishUsageLoad(t, m, cmd)
}

func TestUsageLoadingStaleResultAndRefresh(t *testing.T) {
	m, _ := usageFixture(t)
	cmd := m.showUsage()
	if !m.usage.loading || !strings.Contains(m.usage.viewport.GetContent(), "Loading usage") {
		t.Fatal("missing loading state")
	}
	_, duplicate := m.Update(key('r', "r"))
	if duplicate != nil {
		t.Fatal("refresh started a duplicate read")
	}
	m.Update(key('3', "3"))
	first, ok := cmd().(usageLoaded)
	if !ok {
		t.Fatal("usage command returned an unexpected message")
	}
	m.Update(first)
	if m.usage.days != 90 || !strings.Contains(m.usage.viewport.GetContent(), "Total: 66") {
		t.Fatal("load ignored the latest period selection")
	}
	_, cmd = m.Update(key('r', "r"))
	m.Update(first)
	if !m.usage.loading {
		t.Fatal("stale read completed newer refresh")
	}
	m.Update(key(tea.KeyEscape, ""))
	m.Update(cmd())
	if m.screen != homeScreen {
		t.Fatal("late usage read reopened screen")
	}
}

func TestUsageScrollResizeHelpAndUnsavedExit(t *testing.T) {
	m, _ := usageFixture(t)
	finishUsageLoad(t, m, m.showUsage())
	content := strings.Repeat(m.usage.viewport.GetContent()+"\n", 10)
	m.usage.viewport.SetContent(content)
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 18}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(key(tea.KeyEnd, ""))
		if !m.usage.viewport.AtBottom() {
			t.Fatal("End did not scroll")
		}
		m.Update(key(tea.KeyHome, ""))
		if !m.usage.viewport.AtTop() {
			t.Fatal("Home did not scroll")
		}
		if len(strings.Split(m.View().Content, "\n")) > size[1] {
			t.Fatal("usage view exceeds terminal height")
		}
	}
	m.Update(key(tea.KeyPgDown, ""))
	offset := m.usage.viewport.YOffset()
	m.Update(key('?', "?"))
	if m.screen != viewerScreen {
		t.Fatal("Usage help did not open")
	}
	m.Update(key(tea.KeyEscape, ""))
	if m.screen != usageScreen || m.usage.viewport.YOffset() != offset {
		t.Fatal("help lost usage state")
	}
	m.dirty = true
	m.Update(key('q', "q"))
	if m.screen != confirmScreen {
		t.Fatal("Usage exit discarded config edits")
	}
	m.Update(key(tea.KeyEscape, ""))
	if m.screen != usageScreen {
		t.Fatal("exit cancel did not return to Usage")
	}
}
