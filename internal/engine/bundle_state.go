package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
)

// BundleWork keeps resumable work state separate from content stored by reference.
type BundleWork struct {
	WorkID        string               `json:"workId"`
	ParentWorkID  string               `json:"parentWorkId"`
	Role          string               `json:"role"`
	PreparedRef   runstore.ContentRef  `json:"preparedRef"`
	ResultRef     *runstore.ContentRef `json:"resultRef"`
	Failure       string               `json:"failure"`
	Superseded    bool                 `json:"superseded"`
	InFlight      bool                 `json:"inFlight"`
	Attempts      int                  `json:"attemptCount"`
	RepairUsed    bool                 `json:"repairUsed"`
	CorrectionRef runstore.ContentRef  `json:"correctionRef"`
}

type bundleOutput struct {
	WorkID    string              `json:"workId"`
	Attempt   int                 `json:"attempt"`
	OutputRef runstore.ContentRef `json:"outputRef"`
}

type bundleState struct {
	SchemaVersion   int               `json:"schemaVersion"`
	DocumentSha256  string            `json:"documentSha256"`
	Works           []BundleWork      `json:"works"`
	Attempts        []Attempt         `json:"attempts"`
	Outputs         []bundleOutput    `json:"outputs"`
	StartedAt       time.Time         `json:"startedAt"`
	ArchiveAttempts []json.RawMessage `json:"archiveAttempts"`
}

// BundleArchive combines bundle work and attempts for offline execution inspection.
type BundleArchive struct {
	SchemaVersion  int                 `json:"schemaVersion"`
	Kind           string              `json:"kind"`
	Producer       Producer            `json:"producer"`
	Compatibility  Compatibility       `json:"compatibility"`
	RunID          string              `json:"runId"`
	PassID         string              `json:"passId"`
	PlanHash       string              `json:"planHash"`
	PlanFileSha256 string              `json:"planFileSha256"`
	BaseSha        string              `json:"baseSha"`
	StartSha       string              `json:"startSha"`
	HeadSha        string              `json:"headSha"`
	Status         string              `json:"status"`
	StartedAt      time.Time           `json:"startedAt"`
	ResultRef      runstore.ContentRef `json:"resultRef"`
	Works          []BundleWork        `json:"works"`
	Attempts       []json.RawMessage   `json:"attempts"`
	Outputs        []bundleOutput      `json:"outputs"`
}

// Encode packs execution state into content references for bounded private storage.
func (b *BundleStore) Encode(value any) ([]byte, error) {
	s, ok := value.(state)
	if !ok {
		return nil, errors.New("bundle state type is unsupported")
	}
	stored := bundleState{SchemaVersion: 2, DocumentSha256: b.Manifest.Entries["document"].Sha256, Works: []BundleWork{}, Attempts: s.Attempts, Outputs: []bundleOutput{}, StartedAt: s.StartedAt, ArchiveAttempts: []json.RawMessage{}}
	for _, w := range s.Works {
		prepared, err := b.PackPrepared(w.Prepared)
		if err != nil {
			return nil, err
		}
		correction, err := b.Content.PutText(w.Correction)
		if err != nil {
			return nil, err
		}
		storedWork := BundleWork{w.ID, w.ParentID, w.Role, prepared, nil, w.Failure, w.Superseded, w.InFlight, w.Attempts, w.RepairUsed, correction}
		if w.Result != nil {
			ref, err := b.Content.PutJSON(w.Result)
			if err != nil {
				return nil, err
			}
			storedWork.ResultRef = &ref
		}
		stored.Works = append(stored.Works, storedWork)
	}
	for _, output := range s.Outputs {
		ref, err := b.Content.PutText(output.Output)
		if err != nil {
			return nil, err
		}
		stored.Outputs = append(stored.Outputs, bundleOutput{output.WorkID, output.Attempt, ref})
	}
	for _, attempt := range s.ArchiveAttempts {
		raw, err := b.packAttempt(attempt)
		if err != nil {
			return nil, err
		}
		stored.ArchiveAttempts = append(stored.ArchiveAttempts, raw)
	}
	return json.Marshal(stored)
}

// Decode uses a background context for callers without cancellation support.
func (b *BundleStore) Decode(raw []byte, value any) error {
	return b.DecodeContext(context.Background(), raw, value)
}

// DecodeContext reconstructs execution state from verified bundle references.
func (b *BundleStore) DecodeContext(ctx context.Context, raw []byte, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, ok := value.(*state)
	if !ok {
		return errors.New("bundle state type is unsupported")
	}
	var saved bundleState
	if err := json.Unmarshal(raw, &saved); err != nil {
		return err
	}
	if saved.SchemaVersion != 2 || saved.DocumentSha256 != b.Manifest.Entries["document"].Sha256 {
		return errors.New("bundle state belongs to another shared document")
	}
	s := state{Works: []work{}, Attempts: saved.Attempts, Outputs: []modelOutput{}, StartedAt: saved.StartedAt, ArchiveAttempts: []ArchiveAttempt{}}
	for _, w := range saved.Works {
		prepared, err := b.ReadPreparedContext(ctx, w.PreparedRef)
		if err != nil {
			return err
		}
		correction, err := b.Content.BytesContext(ctx, w.CorrectionRef)
		if err != nil {
			return err
		}
		item := work{ID: w.WorkID, Role: w.Role, Prepared: prepared, Attempts: w.Attempts, RepairUsed: w.RepairUsed, Correction: string(correction), InFlight: w.InFlight, Superseded: w.Superseded, Failure: w.Failure, ParentID: w.ParentWorkID}
		if w.ResultRef != nil {
			var result review.BatchResult
			if err = b.Content.DecodeContext(ctx, *w.ResultRef, &result); err != nil {
				return err
			}
			item.Result = &result
		}
		s.Works = append(s.Works, item)
	}
	for _, output := range saved.Outputs {
		data, err := b.Content.BytesContext(ctx, output.OutputRef)
		if err != nil {
			return err
		}
		s.Outputs = append(s.Outputs, modelOutput{output.WorkID, output.Attempt, string(data)})
	}
	for _, raw := range saved.ArchiveAttempts {
		attempt, err := b.readAttemptContext(ctx, raw)
		if err != nil {
			return err
		}
		s.ArchiveAttempts = append(s.ArchiveAttempts, attempt)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	*target = s
	return nil
}

func (b *BundleStore) packAttempt(attempt ArchiveAttempt) (json.RawMessage, error) {
	values := map[string]string{"systemPrompt": attempt.SystemPrompt, "userPrompt": attempt.UserPrompt, "requestPayload": attempt.RequestPayload, "responsePayload": attempt.ResponsePayload, "rawOutput": attempt.RawOutput, "schema": string(attempt.Schema)}
	metadata := attempt
	metadata.SystemPrompt = ""
	metadata.UserPrompt = ""
	metadata.RequestPayload = ""
	metadata.ResponsePayload = ""
	metadata.RawOutput = ""
	metadata.Schema = nil
	if attempt.Diagnostic != nil {
		cloned := *attempt.Diagnostic
		cloned.RawResponse = ""
		metadata.Diagnostic = &cloned
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for name, text := range values {
		ref, err := b.Content.PutText(text)
		if err != nil {
			return nil, err
		}
		delete(fields, name)
		fields[name+"Ref"], err = json.Marshal(ref)
		if err != nil {
			return nil, err
		}
	}
	if attempt.Diagnostic != nil {
		var diagnostic map[string]json.RawMessage
		if err = json.Unmarshal(fields["diagnostic"], &diagnostic); err != nil {
			return nil, err
		}
		ref, err := b.Content.PutText(attempt.Diagnostic.RawResponse)
		if err != nil {
			return nil, err
		}
		diagnostic["rawResponseRef"], err = json.Marshal(ref)
		if err != nil {
			return nil, err
		}
		fields["diagnostic"], err = json.Marshal(diagnostic)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(fields)
}

func (b *BundleStore) readAttemptContext(ctx context.Context, raw json.RawMessage) (ArchiveAttempt, error) {
	var attempt ArchiveAttempt
	if err := ctx.Err(); err != nil {
		return attempt, err
	}
	if err := json.Unmarshal(raw, &attempt); err != nil {
		return attempt, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return attempt, err
	}
	values := map[string]*string{"systemPrompt": &attempt.SystemPrompt, "userPrompt": &attempt.UserPrompt, "requestPayload": &attempt.RequestPayload, "responsePayload": &attempt.ResponsePayload, "rawOutput": &attempt.RawOutput}
	for name, target := range values {
		var ref runstore.ContentRef
		if err := json.Unmarshal(fields[name+"Ref"], &ref); err != nil {
			return attempt, err
		}
		data, err := b.Content.BytesContext(ctx, ref)
		if err != nil {
			return attempt, err
		}
		*target = string(data)
	}
	var schema runstore.ContentRef
	if err := json.Unmarshal(fields["schemaRef"], &schema); err != nil {
		return attempt, err
	}
	data, err := b.Content.BytesContext(ctx, schema)
	if err != nil {
		return attempt, err
	}
	attempt.Schema = data
	if attempt.Diagnostic != nil {
		var diagnostic struct {
			RawResponseRef runstore.ContentRef `json:"rawResponseRef"`
		}
		if err = json.Unmarshal(fields["diagnostic"], &diagnostic); err != nil {
			return attempt, err
		}
		data, err = b.Content.BytesContext(ctx, diagnostic.RawResponseRef)
		if err != nil {
			return attempt, err
		}
		attempt.Diagnostic.RawResponse = string(data)
	}
	return attempt, nil
}

// ExportArchive exports bundle execution details without requiring another provider call.
func (b *BundleStore) ExportArchive(plan Plan, directory string) (BundleArchive, error) {
	var archive BundleArchive
	store, err := runstore.OpenWithCodec(directory, b.Manifest.PlanHash, b)
	if err != nil {
		return archive, err
	}
	defer store.Close()
	var saved state
	if err = store.Read(&saved); err != nil {
		return archive, err
	}
	if err = validateSavedResults(plan, saved); err != nil {
		return archive, err
	}
	if err = validateArchiveAttempts(saved); err != nil {
		return archive, err
	}
	result := finish(plan, saved)
	if existing, ok := b.Manifest.Entries["result"]; ok {
		var original Result
		if err = b.Content.Decode(existing, &original); err != nil {
			return archive, err
		}
		if original.RunID != plan.RunID || original.PassID != plan.PassID || !sameResultContent(original, result) {
			return archive, errors.New("saved result differs from the journal")
		}
		result = original
	} else if err = b.SaveResult(result); err != nil {
		return archive, err
	}
	packed, err := b.Encode(saved)
	if err != nil {
		return archive, err
	}
	var stateRefs bundleState
	if err = json.Unmarshal(packed, &stateRefs); err != nil {
		return archive, err
	}
	status := result.Status
	for _, w := range saved.Works {
		if w.InFlight || strings.HasPrefix(w.Failure, "interrupted_request:") {
			status = "unknown"
		}
	}
	for _, a := range saved.ArchiveAttempts {
		if a.OutcomeUnknown {
			status = "unknown"
		}
	}
	archive = BundleArchive{2, "ReviewExecutionArchive", Identity(), Contract(), plan.RunID, plan.PassID, b.Manifest.PlanHash, b.Manifest.PlanFileSha256, plan.Document.BaseSha, plan.Document.StartSha, plan.Document.HeadSha, status, saved.StartedAt, b.Manifest.Entries["result"], stateRefs.Works, stateRefs.ArchiveAttempts, stateRefs.Outputs}
	ref, err := b.Content.PutJSON(archive)
	if err != nil {
		return archive, err
	}
	b.Manifest.Entries["archive"] = ref
	b.Manifest.Status = status
	return archive, b.SaveManifest()
}

// PublicArchive removes private attempt data from an archive intended for sharing.
func (b *BundleStore) PublicArchive(plan Plan) (BundleArchive, error) {
	var archive BundleArchive
	ref, ok := b.Manifest.Entries["archive"]
	if !ok {
		return archive, errors.New("public execution archive is missing")
	}
	if err := b.Content.Decode(ref, &archive); err != nil {
		return archive, err
	}
	if archive.SchemaVersion != 2 || archive.Kind != "ReviewExecutionArchive" || archive.Compatibility != Contract() || archive.RunID != plan.RunID || archive.PassID != plan.PassID || archive.PlanHash != b.Manifest.PlanHash || archive.PlanFileSha256 != b.Manifest.PlanFileSha256 || archive.BaseSha != plan.Document.BaseSha || archive.StartSha != plan.Document.StartSha || archive.HeadSha != plan.Document.HeadSha || archive.ResultRef.Sha256 != b.Manifest.Entries["result"].Sha256 {
		return archive, errors.New("public archive belongs to another plan or source")
	}
	var result Result
	if err := b.Content.Decode(archive.ResultRef, &result); err != nil {
		return archive, err
	}
	refs := bundleState{2, b.Manifest.Entries["document"].Sha256, archive.Works, result.Attempts, archive.Outputs, archive.StartedAt, archive.Attempts}
	raw, err := json.Marshal(refs)
	if err != nil {
		return archive, err
	}
	var saved state
	if err = b.Decode(raw, &saved); err != nil {
		return archive, err
	}
	if err = validateSavedResults(plan, saved); err != nil {
		return archive, err
	}
	if err = validateArchiveAttempts(saved); err != nil {
		return archive, err
	}
	if !sameResultContent(result, finish(plan, saved)) {
		return archive, errors.New("public archive result differs from its validated source")
	}
	status := result.Status
	for _, w := range saved.Works {
		if w.InFlight || strings.HasPrefix(w.Failure, "interrupted_request:") {
			status = "unknown"
		}
	}
	for _, a := range saved.ArchiveAttempts {
		if a.OutcomeUnknown {
			status = "unknown"
		}
	}
	if archive.Status != status {
		return archive, errors.New("public archive execution status is invalid")
	}
	return archive, nil
}

// CanResume uses a background context for callers without cancellation support.
func (b *BundleStore) CanResume(plan Plan, directory string) (bool, error) {
	return b.CanResumeContext(context.Background(), plan, directory)
}

// CanResumeContext checks saved work before offering to resume an interrupted review.
func (b *BundleStore) CanResumeContext(ctx context.Context, plan Plan, directory string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	store, err := runstore.OpenWithCodec(directory, b.Manifest.PlanHash, contextBundleCodec{ctx, b})
	if err != nil {
		return false, err
	}
	defer store.Close()
	var saved state
	if err = store.Read(&saved); err != nil {
		return false, err
	}
	if err = validateSavedResultsContext(ctx, plan, saved); err != nil {
		return false, err
	}
	if err = validateArchiveAttempts(saved); err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if _, ok := b.Manifest.Entries["result"]; ok {
		return false, nil
	}
	pending := false
	for _, w := range saved.Works {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		if w.InFlight || strings.HasPrefix(w.Failure, "interrupted_request:") {
			return false, nil
		}
		if !w.Superseded && w.Result == nil && w.Failure == "" {
			pending = true
		}
	}
	for _, a := range saved.ArchiveAttempts {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		if a.OutcomeUnknown {
			return false, nil
		}
	}
	return pending, nil
}

type contextBundleCodec struct {
	ctx    context.Context
	bundle *BundleStore
}

func (c contextBundleCodec) Encode(value any) ([]byte, error) {
	// Keep the request journal writable so an unknown outcome can be recovered.
	return c.bundle.Encode(value)
}

func (c contextBundleCodec) Decode(raw []byte, value any) error {
	return c.bundle.DecodeContext(c.ctx, raw, value)
}

func sameResultContent(a, b Result) bool {
	a.Producer = Producer{}
	b.Producer = Producer{}
	x, ex := json.Marshal(a)
	y, ey := json.Marshal(b)
	return ex == nil && ey == nil && string(x) == string(y)
}
