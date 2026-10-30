package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestEmptyReportIncludesReviewWindowsAndRetention(t *testing.T) {
	report, err := Generate(Store{Path: filepath.Join(t.TempDir(), "private", "stats.sqlite3")}, time.Now().UTC(), []int{7, 30, 90})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		RetentionDays int `json:"retentionDays"`
		Periods       []struct {
			Reviews *struct {
				Total                       int64    `json:"total"`
				AverageDurationMilliseconds *float64 `json:"averageDurationMilliseconds"`
			} `json:"reviews"`
		} `json:"periods"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.RetentionDays != 90 || len(wire.Periods) != 3 {
		t.Fatalf("missing retention or windows: %s", raw)
	}
	for _, p := range wire.Periods {
		if p.Reviews == nil || p.Reviews.Total != 0 || p.Reviews.AverageDurationMilliseconds != nil {
			t.Fatalf("empty review history must be explicit: %s", raw)
		}
	}
}

func TestReviewReportWindowsAndOutcomes(t *testing.T) {
	now := time.Now().UTC()
	store := Store{Path: filepath.Join(t.TempDir(), "private", "stats.sqlite3"), Now: func() time.Time { return now }}
	for i, item := range []struct {
		days    int
		status  string
		seconds int
	}{{1, "complete", 2}, {12, "partial", 4}, {45, "failed", 6}, {2, "canceled", 8}, {1, "unfinished", 0}, {91, "complete", 10}} {
		started := now.Add(-time.Duration(item.days) * 24 * time.Hour)
		r := Review{RunID: fmt.Sprint(i), PassID: "pass", PlanHash: "plan", StartedAt: started, Status: item.status, CheckedBlocks: 1, WarningFindings: 2}
		if item.status != "unfinished" {
			ended := started.Add(time.Duration(item.seconds) * time.Second)
			r.FinishedAt = &ended
		}
		if err := store.SaveReview(r); err != nil {
			t.Fatal(err)
		}
	}
	report, err := Generate(store, now, []int{7, 30, 90})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{3, 4, 5} {
		r := report.Periods[i].Reviews
		if r.Total != want || r.CheckedBlocks != want || r.WarningFindings != want*2 || r.Unfinished != 1 {
			t.Fatalf("bad review window: %+v", r)
		}
	}
	first := report.Periods[0].Reviews
	if first.Complete != 1 || first.Canceled != 1 || first.Partial != 0 || first.AverageDurationMilliseconds == nil || math.Abs(*first.AverageDurationMilliseconds-5000) > 0.001 {
		t.Fatalf("bad outcomes/duration: %+v", first)
	}
	last := report.Periods[2].Reviews
	if last.Partial != 1 || last.Failed != 1 || report.ReviewTrackingSince == nil {
		t.Fatalf("bad review metadata: %+v", last)
	}
}
