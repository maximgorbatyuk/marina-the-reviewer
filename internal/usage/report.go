package usage

import (
	"errors"
	"math"
	"slices"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/stats"
)

// Totals keeps known token totals separate from request count and completeness.
type Totals struct {
	Requests             int64  `json:"requests"`
	FailedRequests       int64  `json:"failedRequests"`
	PendingRequests      int64  `json:"pendingRequests"`
	UnknownUsageRequests int64  `json:"unknownUsageRequests"`
	PartialUsageRequests int64  `json:"partialUsageRequests"`
	InputTokens          *int64 `json:"inputTokens"`
	OutputTokens         *int64 `json:"outputTokens"`
	TotalTokens          *int64 `json:"totalTokens"`
}

// ProviderUsage groups request totals by provider profile and model for reporting.
type ProviderUsage struct {
	ProviderID string   `json:"providerId"`
	Provider   string   `json:"provider"`
	Profiles   []string `json:"profiles"`
	Models     []string `json:"models"`
	Totals
}

// Period retains time bounds and request completeness alongside aggregated usage.
type Period struct {
	Days      int             `json:"days"`
	Since     time.Time       `json:"since"`
	Providers []ProviderUsage `json:"providers"`
	Total     Totals          `json:"total"`
	Reviews   ReviewTotals    `json:"reviews"`
}

// Report keeps usage periods, provider groups, and review outcomes on the same reporting clock.
type Report struct {
	SchemaVersion       int        `json:"schemaVersion"`
	GeneratedAt         time.Time  `json:"generatedAt"`
	TrackingSince       *time.Time `json:"trackingSince"`
	ReviewTrackingSince *time.Time `json:"reviewTrackingSince"`
	RetentionDays       int        `json:"retentionDays"`
	IncompleteTail      bool       `json:"incompleteTail"`
	Periods             []Period   `json:"periods"`
}

// ReviewTotals counts review outcomes separately from provider calls and token totals.
type ReviewTotals struct {
	Total                       int64    `json:"total"`
	Complete                    int64    `json:"complete"`
	Partial                     int64    `json:"partial"`
	Failed                      int64    `json:"failed"`
	Canceled                    int64    `json:"canceled"`
	Unfinished                  int64    `json:"unfinished"`
	CheckedBlocks               int64    `json:"checkedBlocks"`
	WarningFindings             int64    `json:"warningFindings"`
	ErrorFindings               int64    `json:"errorFindings"`
	CriticalFindings            int64    `json:"criticalFindings"`
	AverageDurationMilliseconds *float64 `json:"averageDurationMilliseconds"`
}

func (t *ReviewTotals) count(r stats.Review) error {
	t.Total++
	switch r.Status {
	case "complete":
		t.Complete++
	case "partial":
		t.Partial++
	case "failed":
		t.Failed++
	case "canceled":
		t.Canceled++
	default:
		t.Unfinished++
	}
	for _, field := range []struct {
		sum *int64
		n   int64
	}{
		{&t.CheckedBlocks, r.CheckedBlocks}, {&t.WarningFindings, r.WarningFindings},
		{&t.ErrorFindings, r.ErrorFindings}, {&t.CriticalFindings, r.CriticalFindings},
	} {
		if field.n < 0 || *field.sum > math.MaxInt64-field.n {
			return errors.New("review statistics exceed the supported range")
		}
		*field.sum += field.n
	}
	return nil
}

func add(sum **int64, n *int64) error {
	if n == nil {
		return nil
	}
	if *sum == nil {
		value := int64(0)
		*sum = &value
	}
	if **sum > math.MaxInt64-*n {
		return errors.New("usage token total exceeds the supported range")
	}
	**sum += *n
	return nil
}

func (t *Totals) count(e Event) error {
	t.Requests++
	if e.Kind == "start" {
		t.PendingRequests++
	} else if e.Status != "success" {
		t.FailedRequests++
	}
	c := e.Counts
	if c.Total == nil && c.Input != nil && c.Output != nil {
		if *c.Input > math.MaxInt64-*c.Output {
			return errors.New("usage token total exceeds the supported range")
		}
		value := *c.Input + *c.Output
		c.Total = &value
	}
	if c.Input == nil && c.Output == nil && c.Total == nil {
		t.UnknownUsageRequests++
	} else if c.Input == nil || c.Output == nil || c.Total == nil {
		t.PartialUsageRequests++
	}
	for _, field := range []struct {
		sum **int64
		n   *int64
	}{{&t.InputTokens, c.Input}, {&t.OutputTokens, c.Output}, {&t.TotalTokens, c.Total}} {
		if err := add(field.sum, field.n); err != nil {
			return err
		}
	}
	return nil
}

// Generate builds usage periods from retained statistics while preserving unknown counts and tracking limits.
func Generate(store Store, now time.Time, days []int) (Report, error) {
	maxDays := 7
	for _, d := range days {
		if d != 7 && d != 30 && d != 90 {
			return Report{}, errors.New("usage period must be 7d, 30d or 90d")
		}
		maxDays = max(maxDays, d)
	}
	now = now.UTC()
	snapshot, err := store.Window(now, maxDays)
	if err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: 1, GeneratedAt: now, TrackingSince: snapshot.TrackingSince,
		ReviewTrackingSince: snapshot.ReviewTrackingSince, RetentionDays: 90,
		IncompleteTail: snapshot.IncompleteTail, Periods: []Period{}}
	for _, d := range days {
		period := Period{Days: d, Since: now.Add(-time.Duration(d) * 24 * time.Hour), Providers: []ProviderUsage{}}
		groups := map[string]*ProviderUsage{}
		for _, e := range snapshot.Events {
			if e.StartedAt.Before(period.Since) || e.StartedAt.After(now) {
				continue
			}
			group := groups[e.ProviderID]
			if group == nil {
				group = &ProviderUsage{ProviderID: e.ProviderID, Provider: e.Provider, Profiles: []string{}, Models: []string{}}
				groups[e.ProviderID] = group
			}
			if !slices.Contains(group.Profiles, e.Profile) {
				group.Profiles = append(group.Profiles, e.Profile)
			}
			if !slices.Contains(group.Models, e.Model) {
				group.Models = append(group.Models, e.Model)
			}
			if err := group.count(e); err != nil {
				return Report{}, err
			}
			if err := period.Total.count(e); err != nil {
				return Report{}, err
			}
		}
		var durationSum float64
		var finished int64
		for _, r := range snapshot.Reviews {
			if r.StartedAt.Before(period.Since) || r.StartedAt.After(now) {
				continue
			}
			if err := period.Reviews.count(r); err != nil {
				return Report{}, err
			}
			if r.FinishedAt != nil && r.Status != "unfinished" && !r.FinishedAt.Before(r.StartedAt) {
				durationSum += float64(r.FinishedAt.Sub(r.StartedAt)) / float64(time.Millisecond)
				finished++
			}
		}
		if finished > 0 {
			average := durationSum / float64(finished)
			period.Reviews.AverageDurationMilliseconds = &average
		}
		for _, group := range groups {
			slices.Sort(group.Profiles)
			slices.Sort(group.Models)
			period.Providers = append(period.Providers, *group)
		}
		slices.SortFunc(period.Providers, func(a, b ProviderUsage) int {
			if a.ProviderID < b.ProviderID {
				return -1
			}
			if a.ProviderID > b.ProviderID {
				return 1
			}
			return 0
		})
		report.Periods = append(report.Periods, period)
	}
	return report, nil
}
