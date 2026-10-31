package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorderRecordsOnePhysicalAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
	r := NewRecorder(path)
	ctx := WithOperation(WithRecorder(context.Background(), r), "connection-test")
	finish := Begin(ctx, Metadata{ProviderID: "https://api.example/v1", Provider: "Example", Profile: "backup", Model: "chosen"})
	finish(Counts{Input: number(4), Output: number(2), Total: number(6)}, "invalid_output", Response{})
	finish(Counts{Input: number(4), Output: number(2), Total: number(6)}, "invalid_output", Response{})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	events, _, err := (Store{Path: path}).Read()
	if err != nil || len(events) != 1 || events[0].Operation != "connection-test" {
		t.Fatalf("events %d: %v", len(events), err)
	}
	report, err := Generate(Store{Path: path}, time.Now().UTC(), []int{7})
	if err != nil || report.Periods[0].Total.Requests != 1 || *report.Periods[0].Total.TotalTokens != 6 {
		t.Fatal("attempt duplicated")
	}
}

func TestRecorderFailureIsReportedWithoutFailingAttempt(t *testing.T) {
	r := NewRecorder(t.TempDir())
	finish := Begin(WithRecorder(context.Background(), r), Metadata{ProviderID: "https://api.example/v1", Provider: "Example", Profile: "cloud", Model: "chosen"})
	finish(Counts{}, "network_error", Response{})
	if err := r.Close(); err == nil {
		t.Fatal("write failure was hidden")
	}
}
