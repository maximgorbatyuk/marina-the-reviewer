package usage

import (
	"errors"
	"math"
	"reflect"
	"slices"
	"time"
)

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

type ProviderUsage struct {
	ProviderID string   `json:"providerId"`
	Provider   string   `json:"provider"`
	Profiles   []string `json:"profiles"`
	Models     []string `json:"models"`
	Totals
}

type Period struct {
	Days      int             `json:"days"`
	Since     time.Time       `json:"since"`
	Providers []ProviderUsage `json:"providers"`
	Total     Totals          `json:"total"`
}

type Report struct {
	SchemaVersion  int        `json:"schemaVersion"`
	GeneratedAt    time.Time  `json:"generatedAt"`
	TrackingSince  *time.Time `json:"trackingSince"`
	IncompleteTail bool       `json:"incompleteTail"`
	Periods        []Period   `json:"periods"`
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

func sameRequest(a, b Event) bool {
	return a.StartedAt.Equal(b.StartedAt) && a.ProviderID == b.ProviderID && a.Provider == b.Provider && a.Profile == b.Profile && a.Model == b.Model && a.Operation == b.Operation
}

func Generate(store Store, now time.Time, days []int) (Report, error) {
	events, tail, err := store.Read()
	if err != nil {
		return Report{}, err
	}
	requests := map[string]Event{}
	for _, e := range events {
		previous, ok := requests[e.ID]
		if ok {
			if !sameRequest(previous, e) {
				return Report{}, errors.New("usage journal contains conflicting request identities")
			}
			if previous.Kind == "finish" {
				if e.Kind == "finish" && (!reflect.DeepEqual(e.Counts, previous.Counts) || e.Status != previous.Status) {
					return Report{}, errors.New("usage journal contains conflicting results")
				}
				continue
			}
		}
		requests[e.ID] = e
	}
	now = now.UTC()
	report := Report{SchemaVersion: 1, GeneratedAt: now, IncompleteTail: tail, Periods: []Period{}}
	for _, e := range requests {
		if !e.StartedAt.After(now) && (report.TrackingSince == nil || e.StartedAt.Before(*report.TrackingSince)) {
			at := e.StartedAt
			report.TrackingSince = &at
		}
	}
	for _, d := range days {
		if d != 7 && d != 30 && d != 90 {
			return Report{}, errors.New("usage period must be 7d, 30d or 90d")
		}
		period := Period{Days: d, Since: now.Add(-time.Duration(d) * 24 * time.Hour), Providers: []ProviderUsage{}}
		groups := map[string]*ProviderUsage{}
		for _, e := range requests {
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
			if err := group.Totals.count(e); err != nil {
				return Report{}, err
			}
			if err := period.Total.count(e); err != nil {
				return Report{}, err
			}
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
