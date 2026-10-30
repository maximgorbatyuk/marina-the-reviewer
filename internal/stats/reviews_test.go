package stats

import (
	"testing"
	"time"
)

func TestRepeatedTerminalReviewKeepsOriginalFinish(t *testing.T) {
	for _, status := range []string{"complete", "partial", "failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			s := testStore(t)
			started := fixedTime().Add(-time.Hour)
			finished := started.Add(10 * time.Minute)
			r := Review{RunID: "run", PassID: "pass", PlanHash: "hash", StartedAt: started, FinishedAt: &finished, Status: status, CheckedBlocks: 3, WarningFindings: 1, ErrorFindings: 2, CriticalFindings: 1}
			if err := s.SaveReview(r); err != nil {
				t.Fatal(err)
			}
			later := finished.Add(30 * time.Minute)
			r.FinishedAt = &later
			if err := s.SaveReview(r); err != nil {
				t.Fatal(err)
			}
			snapshot, err := s.Snapshot(fixedTime())
			if err != nil || len(snapshot.Reviews) != 1 {
				t.Fatalf("read review: %+v %v", snapshot, err)
			}
			if snapshot.Reviews[0].FinishedAt == nil || !snapshot.Reviews[0].FinishedAt.Equal(finished) {
				t.Fatal("repeated terminal result changed the original finish time")
			}
		})
	}
}

func TestTerminalReviewProgressUpdatesFinish(t *testing.T) {
	for _, status := range []string{"partial", "failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			s := testStore(t)
			started := fixedTime().Add(-time.Hour)
			finished := started.Add(10 * time.Minute)
			r := Review{RunID: "run", PassID: "pass", PlanHash: "hash", StartedAt: started, FinishedAt: &finished, Status: status, CheckedBlocks: 3}
			if err := s.SaveReview(r); err != nil {
				t.Fatal(err)
			}
			later := finished.Add(30 * time.Minute)
			r.FinishedAt, r.CheckedBlocks = &later, 4
			if err := s.SaveReview(r); err != nil {
				t.Fatal(err)
			}
			snapshot, err := s.Snapshot(fixedTime())
			if err != nil || len(snapshot.Reviews) != 1 || snapshot.Reviews[0].CheckedBlocks != 4 || !snapshot.Reviews[0].FinishedAt.Equal(later) {
				t.Fatalf("progress was not saved: %+v %v", snapshot, err)
			}
			r.Status = "complete"
			latest := later.Add(time.Minute)
			r.FinishedAt = &latest
			if err := s.SaveReview(r); err != nil {
				t.Fatal(err)
			}
			snapshot, err = s.Snapshot(fixedTime())
			if err != nil || snapshot.Reviews[0].Status != "complete" || !snapshot.Reviews[0].FinishedAt.Equal(latest) {
				t.Fatal("changed terminal status was not saved")
			}
		})
	}
}
