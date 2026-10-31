package stats

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyRejectsInvalidSourcesWithoutChangingThem(t *testing.T) {
	for _, fixture := range []struct {
		name, data      string
		public, symlink bool
	}{
		{name: "first-fragment", data: `{"version":1`},
		{name: "unrelated-fragment", data: `{"profiles":{`},
		{name: "unrelated-document", data: "{\"profiles\":{}}\n"},
		{name: "corrupt-complete", data: "broken\n"},
		{name: "long-record", data: strings.Repeat("x", maxRecordBytes+1) + "\n"},
		{name: "public-file", data: "", public: true},
		{name: "symlink", data: "", symlink: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s := testStore(t)
			source := filepath.Join(t.TempDir(), "source.jsonl")
			s.LegacyPath = source
			if err := os.WriteFile(source, []byte(fixture.data), 0600); err != nil {
				t.Fatal(err)
			}
			if fixture.public {
				if err := os.Chmod(source, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if fixture.symlink {
				target := source + ".target"
				if err := os.Rename(source, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, source); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Snapshot(fixedTime()); err == nil {
				t.Fatal("invalid legacy accepted")
			}
			after, err := os.ReadFile(source)
			if err != nil || string(after) != fixture.data {
				t.Fatal("invalid source changed")
			}
		})
	}
}

func TestLegacyConflictRollsBackAndSizeLimitPreservesSource(t *testing.T) {
	s := testStore(t)
	s.LegacyPath = filepath.Join(t.TempDir(), "usage.jsonl")
	e := testEvent("one", fixedTime())
	e.Kind, e.Status, e.Counts = "finish", "success", Counts{Total: ptr(3)}
	conflict := e
	conflict.Counts.Total = ptr(4)
	writeLegacy(t, s.LegacyPath, []Event{e, conflict}, "")
	if _, err := s.Snapshot(fixedTime()); err == nil {
		t.Fatal("conflict accepted")
	}
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM api_requests").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial import: %d %v", count, err)
	}
	_ = db.Close()
	large := filepath.Join(t.TempDir(), "large.jsonl")
	f, err := os.OpenFile(large, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxLedgerBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	s = testStore(t)
	s.LegacyPath = large
	if _, err := s.Snapshot(fixedTime()); err == nil {
		t.Fatal("oversize source accepted")
	}
	info, err := os.Stat(large)
	if err != nil || info.Size() != maxLedgerBytes+1 {
		t.Fatal("oversize source changed")
	}
}

func TestFutureSchemaAndUnsafeSidecarArePreserved(t *testing.T) {
	s := testStore(t)
	if err := s.Append(testEvent("one", fixedTime())); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before, _ := os.ReadFile(s.Path)
	if _, err := s.Snapshot(fixedTime()); err == nil {
		t.Fatal("future schema accepted")
	}
	after, _ := os.ReadFile(s.Path)
	if string(before) != string(after) {
		t.Fatal("future schema changed")
	}
	s = testStore(t)
	if err := s.Append(testEvent("one", fixedTime())); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "sidecar")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, s.Path+"-wal"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(fixedTime()); err == nil {
		t.Fatal("symlink sidecar accepted")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "keep" {
		t.Fatal("sidecar target changed")
	}
}

func TestReviewExpiryDoesNotDeleteRecentRequests(t *testing.T) {
	now := fixedTime()
	s := testStore(t)
	s.Now = func() time.Time { return now }
	r := Review{RunID: "run", PassID: "pass", PlanHash: "plan", StartedAt: now.Add(-90 * 24 * time.Hour), Status: "unfinished"}
	if err := s.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	e := testEvent("one", now)
	e.RunID, e.PassID = r.RunID, r.PassID
	if err := s.Append(e); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	snapshot, err := s.Snapshot(now)
	if err != nil || len(snapshot.Events) != 1 || len(snapshot.Reviews) != 0 {
		t.Fatalf("retention: %+v %v", snapshot, err)
	}
	end := now
	r.Status, r.FinishedAt = "failed", &end
	if err := s.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.Snapshot(now)
	if err != nil || len(snapshot.Reviews) != 0 {
		t.Fatal("expired review returned")
	}
}

func TestLegacyIsNotReadAgainAfterImport(t *testing.T) {
	s := testStore(t)
	s.LegacyPath = filepath.Join(t.TempDir(), "usage.jsonl")
	writeLegacy(t, s.LegacyPath, []Event{testEvent("one", fixedTime())}, "")
	if _, err := s.Snapshot(fixedTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.LegacyPath, []byte("changed external source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(fixedTime())
	if err != nil || len(snapshot.Events) != 1 {
		t.Fatalf("completed import was read again: %v", err)
	}
}

func TestDefaultLegacyRetirementFollowsVerifiedCommit(t *testing.T) {
	s := testStore(t)
	s.LegacyPath = filepath.Join(t.TempDir(), "usage.jsonl")
	s.RetireLegacy = true
	e := testEvent("one", fixedTime())
	e.Kind, e.Status, e.Counts = "finish", "success", Counts{Total: ptr(8)}
	writeLegacy(t, s.LegacyPath, []Event{e}, "")
	snapshot, err := s.Snapshot(fixedTime())
	if err != nil || len(snapshot.Events) != 1 || *snapshot.Events[0].Counts.Total != 8 {
		t.Fatalf("migration: %+v %v", snapshot, err)
	}
	if _, err := os.Stat(s.LegacyPath); !os.IsNotExist(err) {
		t.Fatal("verified default legacy source was retained")
	}
	snapshot, err = s.Snapshot(fixedTime())
	if err != nil || len(snapshot.Events) != 1 {
		t.Fatal("retirement lost imported data")
	}
}

func TestStoragePragmasAndCancelledReview(t *testing.T) {
	s := testStore(t)
	end := fixedTime()
	r := Review{RunID: "run", PassID: "pass", PlanHash: "hash", StartedAt: end.Add(-time.Minute), FinishedAt: &end, Status: "canceled"}
	if err := s.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var mode int
	if err := db.QueryRowContext(t.Context(), "PRAGMA auto_vacuum").Scan(&mode); err != nil || mode != 2 {
		t.Fatalf("auto vacuum: %d %v", mode, err)
	}
	var journal string
	if err := db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal mode: %s %v", journal, err)
	}
	snapshot, err := s.Snapshot(end)
	if err != nil || len(snapshot.Reviews) != 1 || snapshot.Reviews[0].Status != "canceled" {
		t.Fatal("canceled review missing")
	}
}

func TestFutureTrackingDatesBecomeVisibleWhenTimeAdvances(t *testing.T) {
	now := fixedTime()
	s := testStore(t)
	s.Now = func() time.Time { return now }
	at := now.Add(time.Hour)
	if err := s.Append(testEvent("future", at)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReview(Review{RunID: "future", PassID: "pass", PlanHash: "hash", StartedAt: at, Status: "unfinished"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(now)
	if err != nil || snapshot.TrackingSince != nil || snapshot.ReviewTrackingSince != nil || len(snapshot.Events) != 0 || len(snapshot.Reviews) != 0 {
		t.Fatal("future history was included")
	}
	now = at
	snapshot, err = s.Snapshot(now)
	if err != nil || snapshot.TrackingSince == nil || snapshot.ReviewTrackingSince == nil || len(snapshot.Events) != 1 || len(snapshot.Reviews) != 1 {
		t.Fatal("tracking dates did not become visible")
	}
}

func TestProcessAppendHelper(t *testing.T) {
	if os.Getenv("MARINADOREVIEW_STATS_TEST_PROCESS") != "1" {
		return
	}
	s := Store{Path: os.Getenv("MARINADOREVIEW_STATS_TEST_PATH"), Now: fixedTime}
	e := testEvent(os.Getenv("MARINADOREVIEW_STATS_TEST_ID"), fixedTime())
	if err := s.Append(e); err != nil {
		t.Fatal(err)
	}
}

func TestMultipleProcessesInitializeAndAppend(t *testing.T) {
	s := testStore(t)
	commands := make([]*exec.Cmd, 4)
	for i := range commands {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestProcessAppendHelper$")
		cmd.Env = append(os.Environ(), "MARINADOREVIEW_STATS_TEST_PROCESS=1", "MARINADOREVIEW_STATS_TEST_PATH="+s.Path, "MARINADOREVIEW_STATS_TEST_ID="+fmt.Sprint(i), "GORACE=atexit_sleep_ms=0")
		commands[i] = cmd
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.Snapshot(fixedTime())
	if err != nil || len(snapshot.Events) != 4 {
		encoded, _ := json.Marshal(snapshot)
		t.Fatalf("process requests: %s %v", encoded, err)
	}
}
