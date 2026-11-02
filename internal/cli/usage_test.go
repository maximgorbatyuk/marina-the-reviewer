package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func runUsage(t *testing.T, path string, args []string, out, errOut io.Writer) int {
	t.Helper()
	recorder := usage.NewRecorder(path)
	t.Cleanup(func() {
		if err := recorder.Close(); err != nil {
			t.Error(err)
		}
	})
	return RunContext(usage.WithRecorder(t.Context(), recorder), args, &bytes.Buffer{}, out, errOut)
}

func TestUsageCommandPeriodsAndJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
	at := time.Now().UTC()
	value := int64(42)
	if err := (usage.Store{Path: path}).Append(usage.Event{Version: 1, ID: "one", Kind: "finish", At: at, StartedAt: at, ProviderID: "https://provider.example/v1", Provider: "Example", Profile: "cloud", Model: "chosen", Operation: "review", Status: "success", Counts: usage.Counts{Total: &value}}); err != nil {
		t.Fatal(err)
	}
	for _, period := range []string{"", "7d", "30d", "90d"} {
		var out, errOut bytes.Buffer
		args := []string{"usage", "--json"}
		if period != "" {
			args = append(args, "--period", period)
		}
		if code := runUsage(t, path, args, &out, &errOut); code != 0 {
			t.Fatalf("exit %d: %s", code, errOut.String())
		}
		var report usage.Report
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		want := 1
		if period == "" {
			want = 3
		}
		if len(report.Periods) != want || *report.Periods[0].Total.TotalTokens != 42 {
			t.Fatal("wrong report")
		}
	}
	var out, errOut bytes.Buffer
	if code := runUsage(t, path, []string{"usage"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "Example") || !strings.Contains(out.String(), "90 days") {
		t.Fatal("default report missing")
	}
}

func TestUsageArgumentsAndEmptyHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "stats.sqlite3")
	var out, errOut bytes.Buffer
	if code := runUsage(t, path, []string{"usage", "--period", "14d"}, &out, &errOut); code != 2 || out.Len() != 0 {
		t.Fatal("invalid period accepted")
	}
	out.Reset()
	errOut.Reset()
	if code := runUsage(t, path, []string{"usage"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "No API requests recorded") {
		t.Fatal("empty report failed")
	}
}

func TestUsageTextIncludesReviewsWithoutTokenRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
	started := time.Now().UTC().Add(-time.Minute)
	finished := started.Add(2 * time.Second)
	if err := (usage.Store{Path: path}).SaveReview(usage.Review{RunID: "run", PassID: "pass", PlanHash: "plan", StartedAt: started, FinishedAt: &finished, Status: "complete", WarningFindings: 2}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runUsage(t, path, []string{"usage", "--period", "7d"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"Code reviews", "Reviews: 1", "Complete: 1", "Warning: 2", "No API requests recorded"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report missing %q: %s", want, out.String())
		}
	}
}

func TestUsageDoesNotOfferDatabaseSelection(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"usage", "--help"}, &bytes.Buffer{}, &out, &errOut); code != 0 {
		t.Fatalf("help: %d %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "MARINADOREVIEW_DB_FILE") || strings.Contains(out.String(), "MARINADOREVIEW_USAGE_FILE") {
		t.Fatal("help offers a database override")
	}
	for _, command := range []string{"usage", "review", "run"} {
		for _, option := range []string{"--db-file", "--database", "--usage-file"} {
			out.Reset()
			errOut.Reset()
			if code := Run([]string{command, option, "external.sqlite3"}, &bytes.Buffer{}, &out, &errOut); code != 2 {
				t.Fatalf("%s accepted %s: %d %s", command, option, code, errOut.String())
			}
		}
	}
}
