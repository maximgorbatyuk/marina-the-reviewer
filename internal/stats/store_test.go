package stats

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedTime() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
func ptr(n int64) *int64   { return &n }
func testEvent(id string, at time.Time) Event {
	return Event{Version: 1, ID: id, Kind: "start", At: at, StartedAt: at, ProviderID: "https://example.test/v1", Provider: "Example", Profile: "cloud", Model: "model", Operation: "review"}
}
func testStore(t *testing.T) Store {
	t.Helper()
	return Store{Path: filepath.Join(t.TempDir(), "private", "stats.sqlite3"), Now: fixedTime}
}

func TestEmptySnapshotDoesNotCreateStorage(t *testing.T) {
	s := testStore(t)
	snapshot, err := s.Snapshot(fixedTime())
	if err != nil || len(snapshot.Events) != 0 || len(snapshot.Reviews) != 0 {
		t.Fatalf("empty snapshot: %+v, %v", snapshot, err)
	}
	if _, err := os.Stat(filepath.Dir(s.Path)); !os.IsNotExist(err) {
		t.Fatal("empty snapshot created a directory")
	}
}

func TestWindowLoadsOnlyRequestedPeriod(t *testing.T) {
	s := testStore(t)
	for _, days := range []int{1, 8} {
		if err := s.Append(testEvent(fmt.Sprint(days), fixedTime().Add(-time.Duration(days)*24*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Window(fixedTime(), 7)
	if err != nil || len(got.Events) != 1 || got.Events[0].ID != "1" {
		t.Fatalf("window loaded old records: %+v, %v", got, err)
	}
}

func TestSQLiteRequestLifecycleAndPrivacy(t *testing.T) {
	s := testStore(t)
	e := testEvent("one", fixedTime().Add(-time.Hour))
	if err := s.Append(e); err != nil {
		t.Fatal(err)
	}
	e.Kind, e.Status, e.Counts = "finish", "success", Counts{Input: ptr(10), Total: ptr(10)}
	for i := 0; i < 2; i++ {
		if err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Append(testEvent("one", e.StartedAt)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Snapshot(fixedTime())
	if err != nil || len(got.Events) != 1 {
		t.Fatalf("snapshot: %+v %v", got, err)
	}
	if got.Events[0].Kind != "finish" || got.Events[0].Counts.Output != nil || *got.Events[0].Counts.Total != 10 {
		t.Fatal("request fields changed")
	}
	e.Counts.Total = ptr(11)
	if err := s.Append(e); err == nil {
		t.Fatal("conflicting result accepted")
	}
	e = testEvent("one", e.StartedAt)
	e.Model = "different"
	if err := s.Append(e); err == nil {
		t.Fatal("conflicting identity accepted")
	}
	data, err := os.ReadFile(s.Path)
	if err != nil || !strings.HasPrefix(string(data), "SQLite format 3\x00") {
		t.Fatal("storage is not SQLite")
	}
	for _, path := range []string{s.Path, s.Path + ".lock"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("file not private: %s %v", path, err)
		}
	}
}

func TestReviewIdentityAndSnapshot(t *testing.T) {
	s := testStore(t)
	r := Review{RunID: "run", PassID: "pass", PlanHash: "hash", StartedAt: fixedTime().Add(-time.Hour), Status: "unfinished"}
	if err := s.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	end := fixedTime()
	r.Status, r.FinishedAt, r.CheckedBlocks, r.ErrorFindings = "complete", &end, 4, 2
	if err := s.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	r.Status, r.FinishedAt = "unfinished", nil
	if err := s.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Snapshot(end)
	if err != nil || len(got.Reviews) != 1 || got.Reviews[0].Status != "complete" || got.Reviews[0].ErrorFindings != 2 || got.ReviewTrackingSince == nil {
		t.Fatalf("reviews: %+v %v", got, err)
	}
	r.PlanHash = "other"
	if err := s.SaveReview(r); err == nil {
		t.Fatal("conflicting plan accepted")
	}
}

func TestRetentionBoundaryAndLateFinish(t *testing.T) {
	now := fixedTime()
	s := testStore(t)
	s.Now = func() time.Time { return now }
	cutoff := now.Add(-90 * 24 * time.Hour)
	for i, at := range []time.Time{cutoff.Add(-time.Nanosecond), cutoff, now.Add(time.Hour)} {
		if err := s.Append(testEvent(fmt.Sprint(i), at)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Snapshot(now)
	if err != nil || len(got.Events) != 1 || got.Events[0].ID != "1" {
		t.Fatalf("cutoff: %+v %v", got, err)
	}
	now = now.Add(2 * 24 * time.Hour)
	if _, err := s.Snapshot(now); err != nil {
		t.Fatal(err)
	}
	e := testEvent("1", cutoff)
	e.Kind, e.Status = "finish", "success"
	if err := s.Append(e); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM api_requests WHERE request_id = '1'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired request returned: %d %v", count, err)
	}
}

func TestConcurrentRequests(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Append(testEvent(fmt.Sprint(i), fixedTime())); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got, err := s.Snapshot(fixedTime())
	if err != nil || len(got.Events) != 12 {
		t.Fatalf("concurrent requests: %d %v", len(got.Events), err)
	}
}

func writeLegacy(t *testing.T, path string, events []Event, tail string) {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		encoded, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(encoded)
		b.WriteByte('\n')
	}
	b.WriteString(tail)
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyImportIsAtomicAndIdempotent(t *testing.T) {
	s := testStore(t)
	s.LegacyPath = filepath.Join(t.TempDir(), "usage.jsonl")
	e := testEvent("one", fixedTime().Add(-time.Hour))
	finish := e
	finish.Kind, finish.Status, finish.Counts = "finish", "success", Counts{Input: ptr(7), Output: ptr(3)}
	old := testEvent("expired", fixedTime().Add(-91*24*time.Hour))
	writeLegacy(t, s.LegacyPath, []Event{e, finish, finish, old}, `{"version":1`)
	before, _ := os.ReadFile(s.LegacyPath)
	for i := 0; i < 2; i++ {
		got, err := s.Snapshot(fixedTime())
		if err != nil || len(got.Events) != 1 || got.Events[0].Kind != "finish" || !got.IncompleteTail || len(got.Reviews) != 0 {
			t.Fatalf("import: %+v %v", got, err)
		}
	}
	after, _ := os.ReadFile(s.LegacyPath)
	if string(before) != string(after) {
		t.Fatal("explicit legacy source changed")
	}
	bad := testStore(t)
	bad.LegacyPath = filepath.Join(t.TempDir(), "broken.jsonl")
	writeLegacy(t, bad.LegacyPath, []Event{e}, "broken\n")
	if _, err := bad.Snapshot(fixedTime()); err == nil {
		t.Fatal("corrupt legacy accepted")
	}
	writeLegacy(t, bad.LegacyPath, []Event{e}, "")
	got, err := bad.Snapshot(fixedTime())
	if err != nil || len(got.Events) != 1 {
		t.Fatalf("retry import: %+v %v", got, err)
	}
}

func TestStoreRejectsSymlinksAndForeignDatabases(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "stats.sqlite3")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := (Store{Path: path, Now: fixedTime}).Append(testEvent("a", fixedTime())); err == nil {
		t.Fatal("symlink accepted")
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "foreign.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE unrelated (value TEXT); INSERT INTO unrelated VALUES ('keep')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	foreign := filepath.Join(dir, "foreign.sqlite3")
	if err := os.Chmod(foreign, 0600); err != nil {
		t.Error(err)
	}
	before, _ := os.ReadFile(foreign)
	if _, err := (Store{Path: foreign, Now: fixedTime}).Snapshot(fixedTime()); err == nil {
		t.Fatal("foreign database accepted")
	}
	after, _ := os.ReadFile(foreign)
	if string(before) != string(after) {
		t.Fatal("foreign database changed")
	}
}

func TestDefaultStoreOverrides(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "usage.jsonl")
	db := filepath.Join(dir, "chosen.sqlite3")
	t.Setenv("MARINADOREVIEW_USAGE_FILE", old)
	t.Setenv("MARINADOREVIEW_DB_FILE", "")
	s, err := DefaultStore()
	if err != nil || s.Path != old+".sqlite3" || s.LegacyPath != old || s.RetireLegacy {
		t.Fatalf("legacy override: %+v %v", s, err)
	}
	t.Setenv("MARINADOREVIEW_DB_FILE", db)
	s, err = DefaultStore()
	if err != nil || s.Path != db || s.LegacyPath != old {
		t.Fatalf("both overrides: %+v %v", s, err)
	}
	t.Setenv("MARINADOREVIEW_USAGE_FILE", "")
	s, err = DefaultStore()
	if err != nil || s.Path != db || s.LegacyPath != "" {
		t.Fatalf("new override: %+v %v", s, err)
	}
}
