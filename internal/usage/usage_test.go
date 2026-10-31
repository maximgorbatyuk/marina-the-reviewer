package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func number(n int64) *int64 { return &n }

func event(id, kind string, at time.Time) Event {
	return Event{Version: 1, ID: id, Kind: kind, At: at, StartedAt: at, ProviderID: "https://api.example/v1", Provider: "Example", Profile: "cloud", Model: "model", Operation: "review"}
}

func TestStoreIsPrivateAndConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "usage.jsonl")
	store := Store{Path: path}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := event(time.Unix(int64(i), 0).Format(time.RFC3339), "start", time.Now().UTC())
			if err := store.Append(e); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	events, tail, err := store.Read()
	if err != nil || tail || len(events) != 20 {
		t.Fatalf("events %d tail %v: %v", len(events), tail, err)
	}
	info, _ := os.Stat(path)
	dir, _ := os.Stat(filepath.Dir(path))
	if info.Mode().Perm() != 0600 || dir.Mode().Perm() != 0700 {
		t.Fatal("journal not private")
	}
}

func TestStoreRejectsUnsafePathAndUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := (Store{Path: path}).Append(event("a", "start", time.Now().UTC())); err == nil {
		t.Fatal("symlink accepted")
	}
	if data, _ := os.ReadFile(target); string(data) != "keep" {
		t.Fatal("symlink target changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Store{Path: path}).Read(); err == nil {
		t.Fatal("public file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Store{Path: path}).Read(); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestReportWindowsAndDeduplication(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	store := Store{Path: filepath.Join(t.TempDir(), "private", "stats.sqlite3"), Now: func() time.Time { return now }}
	for i, days := range []int{0, 7, 8, 30, 31, 90, 91} {
		at := now.Add(-time.Duration(days) * 24 * time.Hour)
		e := event(string(rune('a'+i)), "start", at)
		if err := store.Append(e); err != nil {
			t.Fatal(err)
		}
		e.Kind = "finish"
		e.Status = "success"
		e.Counts = Counts{Input: number(10), Output: number(2)}
		if err := store.Append(e); err != nil {
			t.Fatal(err)
		}
		if err := store.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	report, err := Generate(store, now, []int{7, 30, 90})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{2, 4, 6} {
		period := report.Periods[i]
		if period.Total.Requests != want || *period.Total.TotalTokens != want*12 || len(period.Providers) != 1 {
			t.Fatalf("period %d counted wrong", period.Days)
		}
	}
}

func TestUnknownPartialPendingAndProviderIdentity(t *testing.T) {
	now := time.Now().UTC()
	s := Store{Path: filepath.Join(t.TempDir(), "private", "stats.sqlite3")}
	for _, id := range []string{"known", "unknown", "pending", "partial", "other"} {
		e := event(id, "start", now)
		if id == "other" {
			e.ProviderID = "https://other.example/v1"
			e.Provider = "Other"
			e.Profile = "cloud"
		}
		if err := s.Append(e); err != nil {
			t.Fatal(err)
		}
		if id == "pending" {
			continue
		}
		e.Kind = "finish"
		e.Status = "invalid_output"
		switch id {
		case "known":
			e.Counts = Counts{Input: number(0), Output: number(5), Total: number(5)}
		case "partial":
			e.Counts = Counts{Input: number(3)}
		}
		if err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Generate(s, now, []int{7})
	if err != nil {
		t.Fatal(err)
	}
	total := r.Periods[0].Total
	if len(r.Periods[0].Providers) != 2 || total.Requests != 5 || total.PendingRequests != 1 || total.UnknownUsageRequests != 3 || total.PartialUsageRequests != 1 || *total.TotalTokens != 5 || *total.InputTokens != 3 {
		t.Fatalf("bad coverage: %+v", total)
	}
	encoded, _ := json.Marshal(r)
	if !strings.Contains(string(encoded), `"inputTokens":null`) {
		t.Fatal("unknown usage was presented as zero")
	}
}

func TestNoHistoryDoesNotCreateFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "usage.jsonl")
	r, err := Generate(Store{Path: path}, time.Now().UTC(), []int{7, 30, 90})
	if err != nil || len(r.Periods) != 3 || r.Periods[0].Total.Requests != 0 {
		t.Fatal("empty history failed")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("read created storage")
	}
}

func TestUsageWriterProcess(t *testing.T) {
	path := os.Getenv("MARINADOREVIEW_TEST_USAGE_PATH")
	if path == "" {
		return
	}
	for i := 0; i < 4; i++ {
		if err := (Store{Path: path}).Append(event(fmt.Sprintf("%d-%d", os.Getpid(), i), "start", time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreConcurrentProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
	commands := []*exec.Cmd{}
	for i := 0; i < 3; i++ {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestUsageWriterProcess$")
		cmd.Env = append(os.Environ(), "MARINADOREVIEW_TEST_USAGE_PATH="+path, "GORACE=atexit_sleep_ms=0")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	events, tail, err := (Store{Path: path}).Read()
	if err != nil || tail || len(events) != 12 {
		t.Fatalf("cross-process writes: %d events, %v", len(events), err)
	}
}

func TestReportRejectsOverflowAndConflictingResults(t *testing.T) {
	for _, kind := range []string{"overflow", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			store := Store{Path: filepath.Join(t.TempDir(), "private", "stats.sqlite3")}
			e := event("first", "finish", time.Now().UTC())
			e.Status = "success"
			e.Counts.Total = number(math.MaxInt64)
			if err := store.Append(e); err != nil {
				t.Fatal(err)
			}
			if kind == "overflow" {
				e.ID = "second"
			}
			e.Counts.Total = number(1)
			err := store.Append(e)
			if kind == "conflict" {
				if err == nil {
					t.Fatal("conflicting result accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Generate(store, time.Now().UTC(), []int{7}); err == nil {
				t.Fatal("invalid total accepted")
			}
		})
	}
}

func TestDefaultUsagePathIsIndependentOfProviderConfig(t *testing.T) {
	t.Setenv("MARINADOREVIEW_USAGE_FILE", "")
	t.Setenv("MARINADOREVIEW_DB_FILE", "")
	first, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARINADOREVIEW_CONFIG", filepath.Join(t.TempDir(), "custom.json"))
	second, err := DefaultPath()
	if err != nil || first != second {
		t.Fatal("config path moved usage storage")
	}
}

func TestAppendPreservesAnUnrelatedPrivateFile(t *testing.T) {
	for _, body := range []string{"{\"version\":1,\"profiles\":{}}\n", "a-private-token-file\n", "{", "{\"version\":1,\"profiles\":{"} {
		path := filepath.Join(t.TempDir(), "existing")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := (Store{Path: path}).Append(event("one", "start", time.Now().UTC())); err == nil {
			t.Fatal("unrelated file accepted as usage journal")
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != body {
			t.Fatal("unrelated file changed")
		}
	}
}
