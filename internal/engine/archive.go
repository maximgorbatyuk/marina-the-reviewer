// Package engine persists review execution state so interrupted work can resume without losing validated results.
package engine

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/ai"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
)

// ExecutionArchiveVersion identifies the execution archive wire format for offline consumers.
const ExecutionArchiveVersion = 1

// ExecutionArchive keeps results and attempts together for inspection without rerunning inference.
type ExecutionArchive struct {
	SchemaVersion  int              `json:"schemaVersion"`
	Kind           string           `json:"kind"`
	Producer       Producer         `json:"producer"`
	Compatibility  Compatibility    `json:"compatibility"`
	RunID          string           `json:"runId"`
	PassID         string           `json:"passId"`
	PlanHash       string           `json:"planHash"`
	PlanFileSha256 string           `json:"planFileSha256"`
	BaseSha        string           `json:"baseSha"`
	StartSha       string           `json:"startSha"`
	HeadSha        string           `json:"headSha"`
	Status         string           `json:"status"`
	StartedAt      time.Time        `json:"startedAt"`
	Result         Result           `json:"result"`
	Works          []ArchiveWork    `json:"works"`
	Attempts       []ArchiveAttempt `json:"attempts"`
}

// ArchiveWork preserves the state of each work item so archive readers can explain incomplete runs.
type ArchiveWork struct {
	WorkID       string               `json:"workId"`
	ParentWorkID string               `json:"parentWorkId"`
	Role         string               `json:"role"`
	Prepared     review.PreparedBatch `json:"prepared"`
	Result       *review.BatchResult  `json:"result"`
	Failure      string               `json:"failure"`
	Superseded   bool                 `json:"superseded"`
	InFlight     bool                 `json:"inFlight"`
}

// ArchiveDiagnostic retains a safe failure description alongside provider response diagnostics.
type ArchiveDiagnostic struct {
	Message                string `json:"message"`
	StatusCode             int    `json:"statusCode,omitempty"`
	RetryAfterMilliseconds int64  `json:"retryAfterMilliseconds,omitempty"`
	RawResponse            string `json:"rawResponse,omitempty"`
}

// ArchiveAttempt records request and response details so an inference attempt can be inspected offline.
type ArchiveAttempt struct {
	WorkID                   string             `json:"workId"`
	Number                   int                `json:"number"`
	Phase                    string             `json:"phase"`
	RequestID                string             `json:"requestId"`
	SystemPrompt             string             `json:"systemPrompt"`
	UserPrompt               string             `json:"userPrompt"`
	Endpoint                 string             `json:"endpoint"`
	RequestPayload           string             `json:"requestPayload"`
	ResponsePayload          string             `json:"responsePayload"`
	ResponsePayloadTruncated bool               `json:"responsePayloadTruncated"`
	Schema                   json.RawMessage    `json:"schema"`
	RawOutput                string             `json:"rawOutput"`
	Provider                 string             `json:"provider"`
	Model                    string             `json:"model"`
	Usage                    ai.Usage           `json:"usage"`
	FinishReason             string             `json:"finishReason"`
	Channel                  string             `json:"channel"`
	Code                     string             `json:"code"`
	Diagnostic               *ArchiveDiagnostic `json:"diagnostic"`
	StartedAt                time.Time          `json:"startedAt"`
	ElapsedMilliseconds      int64              `json:"elapsedMilliseconds"`
	OutcomeUnknown           bool               `json:"outcomeUnknown"`
}

func (archive ExecutionArchive) Write(path string) error {
	return runstore.AtomicJSONLimit(path, archive, runstore.MaximumExecutionDocumentBytes)
}

// ExportArchive validates saved run state before constructing an execution archive.
func ExportArchive(directory string) (ExecutionArchive, error) {
	raw, err := runstore.ReadPrivate(filepath.Join(directory, "plan.json"), runstore.MaximumDocumentBytes)
	if err != nil {
		return ExecutionArchive{}, err
	}
	var plan Plan
	if json.Unmarshal(raw, &plan) != nil {
		return ExecutionArchive{}, errors.New("saved plan is invalid")
	}
	if err = plan.Validate(); err != nil {
		return ExecutionArchive{}, err
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return ExecutionArchive{}, errors.New("saved plan cannot be serialized")
	}
	planHash := review.Hash(string(encoded))
	store, err := runstore.Open(directory, planHash)
	if err != nil {
		return ExecutionArchive{}, err
	}
	defer store.Close()
	var saved state
	if err = store.Read(&saved); err != nil {
		return ExecutionArchive{}, err
	}
	if err = validateSavedResults(plan, saved); err != nil {
		return ExecutionArchive{}, err
	}
	if err = validateArchiveAttempts(saved); err != nil {
		return ExecutionArchive{}, err
	}
	archive := ExecutionArchive{SchemaVersion: ExecutionArchiveVersion, Kind: "ReviewExecutionArchive", Producer: Identity(),
		Compatibility: Contract(), RunID: plan.RunID, PassID: plan.PassID, PlanHash: planHash, PlanFileSha256: review.Hash(string(raw)),
		BaseSha: plan.Document.BaseSha, StartSha: plan.Document.StartSha, HeadSha: plan.Document.HeadSha,
		Status: finish(plan, saved).Status, StartedAt: saved.StartedAt, Result: finish(plan, saved),
		Works: []ArchiveWork{}, Attempts: append([]ArchiveAttempt{}, saved.ArchiveAttempts...)}
	for _, item := range saved.Works {
		archive.Works = append(archive.Works, ArchiveWork{WorkID: item.ID, ParentWorkID: item.ParentID, Role: item.Role,
			Prepared: item.Prepared, Result: item.Result, Failure: item.Failure, Superseded: item.Superseded, InFlight: item.InFlight})
		if item.InFlight || strings.HasPrefix(item.Failure, "interrupted_request:") {
			archive.Status = "unknown"
		}
	}
	for i := range archive.Attempts {
		if archive.Attempts[i].OutcomeUnknown {
			archive.Status = "unknown"
			archive.Attempts[i].Code = "interrupted_request"
		}
	}
	encoded, err = json.Marshal(archive)
	if err != nil || len(encoded) > runstore.MaximumExecutionDocumentBytes {
		return ExecutionArchive{}, errors.New("execution archive exceeds the document size limit or cannot be encoded")
	}
	return archive, nil
}

func validateArchiveAttempts(saved state) error {
	works := map[string]work{}
	for _, item := range saved.Works {
		works[item.ID] = item
	}
	requests, attempts := map[string]bool{}, map[string]ArchiveAttempt{}
	for _, attempt := range saved.ArchiveAttempts {
		item, exists := works[attempt.WorkID]
		if !exists || attempt.Number < 1 || attempt.Number > item.Attempts || requests[attempt.RequestID] || attempt.StartedAt.IsZero() {
			return errors.New("saved archive has an invalid request identity")
		}
		if _, err := review.FindingID(attempt.RequestID, attempt.RequestID, ""); err != nil {
			return errors.New("saved archive has an invalid physical request ID")
		}
		key := attempt.WorkID + ":" + strconv.Itoa(attempt.Number)
		if _, exists := attempts[key]; exists {
			return errors.New("saved archive has duplicate attempts")
		}
		requests[attempt.RequestID], attempts[key] = true, attempt
	}
	for _, attempt := range saved.Attempts {
		archived, exists := attempts[attempt.WorkID+":"+strconv.Itoa(attempt.Number)]
		if !exists || archived.Code != attempt.Code || archived.Phase != attempt.Phase || !archived.StartedAt.Equal(attempt.StartedAt) {
			return errors.New("saved run has no complete execution archive for its attempts")
		}
	}
	return nil
}
