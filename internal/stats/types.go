package stats

import "time"

// RetentionDays bounds how long request and review statistics remain in local storage.
const RetentionDays = 90

// Counts keeps unknown token counts distinct from measured zero values.
type Counts struct {
	Input  *int64 `json:"inputTokens"`
	Output *int64 `json:"outputTokens"`
	Total  *int64 `json:"totalTokens"`
}

// Event pairs request start and finish metadata for reliable local usage accounting.
type Event struct {
	Version          int       `json:"version"`
	ID               string    `json:"requestId"`
	Kind             string    `json:"kind"`
	At               time.Time `json:"at"`
	StartedAt        time.Time `json:"startedAt"`
	ProviderID       string    `json:"providerId"`
	Provider         string    `json:"provider"`
	ProviderKind     string    `json:"providerKind,omitempty"`
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

// Review records one review outcome independently of its provider request count.
type Review struct {
	RunID, PassID, PlanHash                                         string
	StartedAt                                                       time.Time
	FinishedAt                                                      *time.Time
	Status                                                          string
	CheckedBlocks, WarningFindings, ErrorFindings, CriticalFindings int64
}

// Snapshot keeps reporting data with the earliest known tracking timestamps.
type Snapshot struct {
	Events                             []Event
	Reviews                            []Review
	TrackingSince, ReviewTrackingSince *time.Time
	IncompleteTail                     bool
}

// Store selects private statistics storage and legacy import behavior without storing credentials.
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
