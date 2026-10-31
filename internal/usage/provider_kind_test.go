package usage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProviderKindsPreserveEndpointGrouping(t *testing.T) {
	now := time.Now().UTC()
	store := Store{Path: filepath.Join(t.TempDir(), "private", "stats.sqlite3")}
	for _, kind := range []string{"deepseek", "openai-compatible"} {
		e := event(kind, "finish", now)
		e.ProviderKind, e.Profile, e.Status, e.Counts = kind, kind, "success", Counts{Total: number(3)}
		if err := store.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.Snapshot(now)
	if err != nil || len(snapshot.Events) != 2 || snapshot.Events[0].ProviderKind != "deepseek" || snapshot.Events[1].ProviderKind != "openai-compatible" {
		t.Fatal("profiles on the same endpoint lost their selected provider kinds")
	}
	report, err := Generate(store, now, []int{7})
	if err != nil || len(report.Periods[0].Providers) != 1 || report.Periods[0].Total.Requests != 2 || *report.Periods[0].Total.TotalTokens != 6 {
		t.Fatalf("provider kinds changed endpoint grouping or totals: %+v %v", report, err)
	}
}
