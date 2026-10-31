package stats

import "time"

const RetentionDays = 90

type Counts struct {
	Input  *int64 `json:"inputTokens"`
	Output *int64 `json:"outputTokens"`
	Total  *int64 `json:"totalTokens"`
}

type Event struct {
	Version          int       `json:"version"`
	ID               string    `json:"requestId"`
	Kind             string    `json:"kind"`
	At               time.Time `json:"at"`
	StartedAt        time.Time `json:"startedAt"`
	ProviderID       string    `json:"providerId"`
	Provider         string    `json:"provider"`
	Profile          string    `json:"profile"`
	Model            string    `json:"model"`
	Operation        string    `json:"operation"`
	Status           string    `json:"status,omitempty"`
	Counts           Counts    `json:"usage"`
	RunID            string    `json:"runId,omitempty"`
	PassID           string    `json:"passId,omitempty"`
	RequestURL       string    `json:"-"`
	RequestPayload   []byte    `json:"-"`
	ResponsePayload  []byte    `json:"-"`
	HTTPStatus       int       `json:"-"`
	ResponseComplete *bool     `json:"-"`
}

type Review struct {
	RunID, PassID, PlanHash                                         string
	StartedAt                                                       time.Time
	FinishedAt                                                      *time.Time
	Status                                                          string
	CheckedBlocks, WarningFindings, ErrorFindings, CriticalFindings int64
}

type Snapshot struct {
	Events                             []Event
	Reviews                            []Review
	TrackingSince, ReviewTrackingSince *time.Time
	IncompleteTail                     bool
}

type Store struct {
	Path         string
	LegacyPath   string
	RetireLegacy bool
	Now          func() time.Time
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

const timestampLayout = "2006-01-02T15:04:05.000000000Z"

func stamp(t time.Time) string                   { return t.UTC().Format(timestampLayout) }
func parseStamp(value string) (time.Time, error) { return time.Parse(timestampLayout, value) }
