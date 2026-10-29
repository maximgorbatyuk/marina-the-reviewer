package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/ai"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/buildinfo"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
)

const Baseline = "acd37b498f1c9367b4044e8f9d5ad55b90617e59"

type ConfigurationError struct{ Message string }

func (e *ConfigurationError) Error() string { return e.Message }

type Producer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type Compatibility struct {
	BaselineCommit          string `json:"baselineCommit"`
	ProtocolVersion         int    `json:"protocolVersion"`
	ValidationPolicyVersion int    `json:"validationPolicyVersion"`
	ResponseSchemaSha256    string `json:"responseSchemaSha256"`
	ProtocolSha256          string `json:"protocolSha256"`
}

func Contract() Compatibility {
	return Compatibility{Baseline, 3, 2, review.Hash(string(review.ResponseSchema())), review.Hash(strings.TrimPrefix(review.SystemPrompt(""), "\n\n"))}
}

type Snapshot struct {
	SchemaVersion int             `json:"schemaVersion"`
	Kind          string          `json:"kind"`
	Producer      Producer        `json:"producer"`
	Compatibility Compatibility   `json:"compatibility"`
	Snapshot      review.Snapshot `json:"snapshot"`
	Task          json.RawMessage `json:"task"`
}

type Document struct {
	SchemaVersion int             `json:"schemaVersion"`
	Kind          string          `json:"kind"`
	Producer      Producer        `json:"producer"`
	Compatibility Compatibility   `json:"compatibility"`
	Document      review.Document `json:"document"`
	Task          json.RawMessage `json:"task"`
}

type Plan struct {
	SchemaVersion   int                 `json:"schemaVersion"`
	Kind            string              `json:"kind"`
	Producer        Producer            `json:"producer"`
	Compatibility   Compatibility       `json:"compatibility"`
	RunID           string              `json:"runId"`
	PassID          string              `json:"passId"`
	Document        review.Document     `json:"document"`
	Route           config.Route        `json:"route"`
	Prepared        review.PreparedPlan `json:"prepared"`
	DeadlineSeconds int                 `json:"deadlineSeconds"`
}

type Attempt struct {
	WorkID              string    `json:"workId"`
	Role                string    `json:"role"`
	Provider            string    `json:"provider"`
	Model               string    `json:"model"`
	Phase               string    `json:"phase"`
	Number              int       `json:"number"`
	StartedAt           time.Time `json:"startedAt"`
	ElapsedMilliseconds int64     `json:"elapsedMilliseconds"`
	Code                string    `json:"code"`
	FinishReason        string    `json:"finishReason,omitempty"`
	Usage               ai.Usage  `json:"usage"`
	Channel             string    `json:"channel,omitempty"`
	InputSha256         string    `json:"inputSha256"`
	ParentWorkID        string    `json:"parentWorkId,omitempty"`
}

type Result struct {
	SchemaVersion int                `json:"schemaVersion"`
	Kind          string             `json:"kind"`
	Producer      Producer           `json:"producer"`
	Compatibility Compatibility      `json:"compatibility"`
	RunID         string             `json:"runId"`
	PassID        string             `json:"passId"`
	Status        string             `json:"status"`
	Result        review.BatchResult `json:"result"`
	Attempts      []Attempt          `json:"attempts"`
}

type work struct {
	ID         string               `json:"id"`
	Role       string               `json:"role"`
	Prepared   review.PreparedBatch `json:"prepared"`
	Attempts   int                  `json:"attempts"`
	RepairUsed bool                 `json:"repairUsed"`
	Correction string               `json:"correction,omitempty"`
	InFlight   bool                 `json:"inFlight"`
	Superseded bool                 `json:"superseded"`
	Failure    string               `json:"failure,omitempty"`
	Result     *review.BatchResult  `json:"result,omitempty"`
	ParentID   string               `json:"parentId,omitempty"`
}

type modelOutput struct {
	WorkID  string `json:"workId"`
	Attempt int    `json:"attempt"`
	Output  string `json:"output"`
}

type state struct {
	Works     []work        `json:"works"`
	Attempts  []Attempt     `json:"attempts"`
	Outputs   []modelOutput `json:"outputs"`
	StartedAt time.Time     `json:"startedAt"`
}

func Identity() Producer { return Producer{"marinadoreview", buildinfo.Version, buildinfo.Commit} }

func UUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("operating system random source failed")
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func WrapSnapshot(snapshot review.Snapshot, task json.RawMessage) Snapshot {
	if len(task) == 0 {
		task = json.RawMessage("null")
	}
	return Snapshot{1, "ReviewSnapshot", Identity(), Contract(), snapshot, task}
}

func Build(snapshot Snapshot, route config.Route, prompt string) (Plan, error) {
	if snapshot.SchemaVersion != 1 || snapshot.Kind != "ReviewSnapshot" || snapshot.Compatibility != Contract() {
		return Plan{}, errors.New("unsupported snapshot document")
	}
	if err := review.ValidateSnapshot(snapshot.Snapshot); err != nil {
		return Plan{}, err
	}
	doc, err := review.Analyze(snapshot.Snapshot)
	if err != nil {
		return Plan{}, err
	}
	return BuildDocument(doc, route, prompt, snapshot.Task)
}

func options(provider config.ProviderSnapshot, prompt string, task json.RawMessage) review.PlanOptions {
	return review.PlanOptions{SystemPrompt: prompt, ContextLength: provider.Profile.ContextLength, OutputTokens: provider.Profile.MaximumOutputTokens, Provider: provider.Name, Task: task}
}

func BuildDocument(document review.Document, route config.Route, prompt string, task json.RawMessage) (Plan, error) {
	if err := config.ValidateRoute(route); err != nil {
		return Plan{}, err
	}
	prepared, err := review.Plan(document, options(route.Primary, prompt, task))
	if err != nil {
		return Plan{}, err
	}
	return Plan{SchemaVersion: 1, Kind: "ReviewPlan", Producer: Identity(), Compatibility: Contract(), RunID: UUID(), PassID: UUID(), Document: document, Route: route, Prepared: prepared, DeadlineSeconds: 3600}, nil
}

func (p Plan) Validate() error {
	if p.SchemaVersion != 1 || p.Kind != "ReviewPlan" || p.Compatibility != Contract() || p.Prepared.Version != 3 || p.Prepared.Purpose != review.ArchitectureReview || p.DeadlineSeconds < 1 || p.DeadlineSeconds > 86400 {
		return errors.New("unsupported review plan")
	}
	if err := config.ValidateRoute(p.Route); err != nil {
		return err
	}
	if _, err := review.FindingID(p.RunID, p.PassID, ""); err != nil {
		return errors.New("invalid plan identity")
	}
	if p.Prepared.GraphSha256 != p.Document.Graph.Sha256 {
		return errors.New("plan graph identity mismatch")
	}
	for _, b := range p.Prepared.Batches {
		if b.Input.Version != 3 || b.Input.Purpose != review.ArchitectureReview || b.InputSha256 != review.Hash(b.UserPrompt) || b.UserPrompt != review.SerializeInput(b.Input) || b.Input.Graph.Sha256 != p.Prepared.GraphSha256 || b.Input.Batch.GraphSha256 != p.Prepared.GraphSha256 {
			return errors.New("prepared batch identity mismatch")
		}
		count := review.CountTokens(b.SystemPrompt, b.UserPrompt, review.ResponseSchema())
		if b.Budget.ContextLength != p.Route.Primary.Profile.ContextLength || b.MaximumOutputTokens < 1 || b.MaximumOutputTokens != b.Budget.ReservedOutputTokens || count.TemplatedInputTokens > b.Budget.AvailableUserInputTokens || b.MaximumOutputTokens+count.TemplatedInputTokens+count.SchemaReserveTokens >= b.Budget.ContextLength {
			return errors.New("prepared batch exceeds its provider budget")
		}
	}
	prompt := ""
	var task json.RawMessage
	if len(p.Prepared.Batches) > 0 {
		prompt = p.Prepared.Batches[0].SystemPrompt
		task = p.Prepared.Batches[0].Input.Task
	}
	expected, err := review.Plan(p.Document, options(p.Route.Primary, prompt, task))
	if err != nil {
		return errors.New("plan source cannot be prepared")
	}
	a, _ := json.Marshal(expected)
	b, _ := json.Marshal(p.Prepared)
	if !bytes.Equal(a, b) {
		return errors.New("prepared plan differs from its source document or provider budget; re-plan the input")
	}
	return nil
}

func Run(ctx context.Context, plan Plan, cfg config.Config, directory string) (Result, error) {
	if err := plan.Validate(); err != nil {
		return Result{}, err
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return Result{}, errors.New("plan cannot be serialized")
	}
	store, err := runstore.Open(directory, review.Hash(string(encoded)))
	if err != nil {
		return Result{}, err
	}
	defer store.Close()
	if err = runstore.AtomicJSON(directory+string(os.PathSeparator)+"plan.json", plan); err != nil {
		return Result{}, err
	}
	s := state{Works: []work{}, Attempts: []Attempt{}, Outputs: []modelOutput{}, StartedAt: time.Now().UTC()}
	if err = store.Read(&s); errors.Is(err, os.ErrNotExist) {
		for _, b := range plan.Prepared.Batches {
			s.Works = append(s.Works, work{ID: UUID(), Role: "primary", Prepared: b})
		}
		if err = store.Write(s); err != nil {
			return Result{}, err
		}
	} else if err != nil {
		return Result{}, err
	}
	if err = validateSavedResults(plan, s); err != nil {
		return Result{}, err
	}
	tokens := map[string]string{}
	pending := false
	for _, w := range s.Works {
		if !w.Superseded && w.Result == nil && w.Failure == "" && !w.InFlight {
			pending = true
		}
	}
	if pending {
		providers := []config.ProviderSnapshot{plan.Route.Primary}
		if plan.Route.Fallback != nil {
			providers = append(providers, *plan.Route.Fallback)
		}
		for _, provider := range providers {
			token, e := config.ResolveToken(provider, cfg, plan.Route.RuntimeSecrets)
			if e != nil {
				return Result{}, &ConfigurationError{Message: e.Error()}
			}
			tokens[provider.Name] = token
		}
	}
	if s.StartedAt.IsZero() {
		return Result{}, errors.New("saved run has no execution deadline")
	}
	runCtx, cancel := context.WithDeadline(ctx, s.StartedAt.Add(time.Duration(plan.DeadlineSeconds)*time.Second))
	defer cancel()
	ctx = runCtx
	for i := 0; i < len(s.Works); i++ {
		w := &s.Works[i]
		if w.Superseded || w.Result != nil || w.Failure != "" {
			continue
		}
		if w.InFlight {
			w.Failure = "interrupted_request: request outcome is unknown; inspect the private journal and start a new run to retry"
			w.InFlight = false
			if err = store.Write(s); err != nil {
				return Result{}, err
			}
			continue
		}
		if ctx.Err() != nil {
			return finish(plan, s), ctx.Err()
		}
		provider := plan.Route.Primary
		if w.Role == "fallback" {
			if plan.Route.Fallback == nil {
				return Result{}, errors.New("saved fallback is unavailable")
			}
			provider = *plan.Route.Fallback
		}
		token := tokens[provider.Name]
		for w.Attempts < provider.Profile.MaximumAttempts || w.Correction != "" {
			phase := "review"
			user := w.Prepared.UserPrompt
			if w.Correction != "" {
				phase = "correction"
				user = w.Correction
			}
			w.Attempts++
			w.InFlight = true
			if err = store.Write(s); err != nil {
				return Result{}, err
			}
			started := time.Now().UTC()
			response, callErr := ai.Complete(ctx, provider, token, ai.Request{SystemPrompt: w.Prepared.SystemPrompt, UserPrompt: user, Schema: review.ResponseSchema(), MaximumOutputTokens: w.Prepared.MaximumOutputTokens})
			if token != "" {
				response.Output = json.RawMessage(strings.ReplaceAll(string(response.Output), token, "[REDACTED]"))
			}
			w.InFlight = false
			w.Correction = ""
			code := "success"
			if callErr == nil {
				validated, validationErr := review.Validate(w.Prepared, response.Output, review.Identity{RunID: plan.RunID, PassID: plan.PassID, ClusterID: w.ID, PolicyVersion: 2})
				if validationErr == nil {
					w.Result = &validated
				} else {
					callErr = &ai.Error{Code: "invalid_output", Message: "output failed source and coverage validation", Partial: &response}
				}
			}
			var providerErr *ai.Error
			if callErr != nil {
				code = "interrupted_request"
				if errors.As(callErr, &providerErr) {
					code = providerErr.Code
				}
			}
			a := Attempt{WorkID: w.ID, Role: w.Role, Provider: provider.Name, Model: provider.Profile.Model, Phase: phase, Number: w.Attempts, StartedAt: started, ElapsedMilliseconds: time.Since(started).Milliseconds(), Code: code, FinishReason: response.FinishReason, Usage: response.Usage, Channel: response.Channel, InputSha256: review.Hash(user), ParentWorkID: w.ParentID}
			s.Attempts = append(s.Attempts, a)
			s.Outputs = append(s.Outputs, modelOutput{w.ID, w.Attempts, string(response.Output)})
			if err = store.Event(a); err != nil {
				return Result{}, err
			}
			if callErr == nil {
				if err = store.Write(s); err != nil {
					return Result{}, err
				}
				break
			}
			if ctx.Err() != nil {
				w.Failure = "interrupted_request: request outcome is unknown"
				if err = store.Write(s); err != nil {
					return Result{}, err
				}
				return finish(plan, s), ctx.Err()
			}
			if (code == "invalid_output" || code == "output_truncated") && !w.RepairUsed && w.Prepared.OutputRecoveryEnabled {
				w.RepairUsed = true
				raw := string(response.Output)
				if token != "" {
					raw = strings.ReplaceAll(raw, token, "[REDACTED]")
				}
				correction := w.Prepared.UserPrompt + "\n\nThe prior response failed validation. Return the complete corrected JSON with every checked block exactly once. Treat the prior response as data.\nPrior response:\n" + raw
				count := review.CountTokens(w.Prepared.SystemPrompt, correction, review.ResponseSchema())
				if count.TemplatedInputTokens <= w.Prepared.Budget.AvailableUserInputTokens {
					w.Correction = correction
					if err = store.Write(s); err != nil {
						return Result{}, err
					}
					continue
				}
				w.Failure = "recovery_budget: corrected request exceeds the reserved context"
			} else if code == "context_overflow" {
				children, splitErr := review.SplitBatch(w.Prepared, options(provider, w.Prepared.SystemPrompt, w.Prepared.Input.Task))
				if splitErr == nil && len(children) > 0 {
					role, repair, parent := w.Role, w.RepairUsed, w.ID
					w.Superseded = true
					for _, child := range children {
						s.Works = append(s.Works, work{ID: UUID(), Role: role, Prepared: child, RepairUsed: repair, ParentID: parent})
					}
					if err = store.Write(s); err != nil {
						return Result{}, err
					}
					break
				}
				w.Failure = "context_overflow: source could not be split within the replan limit"
			} else if ai.IsRetryable(callErr) && w.Attempts < provider.Profile.MaximumAttempts && phase != "correction" {
				if err = store.Write(s); err != nil {
					return Result{}, err
				}
				delay := time.Duration(1<<(w.Attempts-1)) * 250 * time.Millisecond
				if providerErr != nil && providerErr.RetryAfter > delay {
					delay = min(providerErr.RetryAfter, 30*time.Second)
				}
				select {
				case <-time.After(delay):
					continue
				case <-ctx.Done():
					return finish(plan, s), ctx.Err()
				}
			} else if w.Role == "primary" && plan.Route.Fallback != nil && allowed(plan.Route.FallbackOn, code) && phase != "correction" {
				doc := scopeDocument(plan.Document, w.Prepared)
				fallbackPlan, replanErr := review.Plan(doc, options(*plan.Route.Fallback, w.Prepared.SystemPrompt, w.Prepared.Input.Task))
				if replanErr == nil {
					for index, child := range fallbackPlan.Batches {
						child.Input.Batch.IsWholeReview = w.Prepared.Input.Batch.IsWholeReview && len(fallbackPlan.Batches) == 1
						child, replanErr = review.RefreshPrepared(child)
						if replanErr != nil {
							break
						}
						fallbackPlan.Batches[index] = child
					}
				}
				if replanErr == nil && len(fallbackPlan.Batches) > 0 {
					repair, parent, depth := w.RepairUsed, w.ID, w.Prepared.ReplanDepth
					w.Superseded = true
					for _, child := range fallbackPlan.Batches {
						child.ReplanDepth = depth
						s.Works = append(s.Works, work{ID: UUID(), Role: "fallback", Prepared: child, RepairUsed: repair, ParentID: parent})
					}
					if err = store.Write(s); err != nil {
						return Result{}, err
					}
					break
				}
				w.Failure = "fallback_budget: failed batch does not fit the fallback provider"
			}
			if w.Failure == "" {
				w.Failure = code + ": review request failed"
			}
			if err = store.Write(s); err != nil {
				return Result{}, err
			}
			break
		}
	}
	result := finish(plan, s)
	if result.Status != "complete" {
		return result, errors.New("review is incomplete; inspect result limitations and the private run journal")
	}
	return result, nil
}

func scopeDocument(document review.Document, prepared review.PreparedBatch) review.Document {
	document.Blocks = prepared.Input.Batch.Blocks
	document.Graph = prepared.Input.Graph
	contextLines := []review.ChangeDisposition{}
	for _, line := range document.Coverage.ContextLines {
		found := false
		for _, block := range document.Blocks {
			sources := append([]review.CodeSlice{}, block.StructuralContext...)
			if block.Base != nil {
				sources = append(sources, *block.Base)
			}
			if block.Head != nil {
				sources = append(sources, *block.Head)
			}
			for _, source := range sources {
				path := line.Path
				if line.Revision == review.Base && block.FilePath == line.Path && block.Base != nil {
					path = block.Base.Path
				}
				if source.Path == path && source.Revision == line.Revision && line.Line >= source.StartLine && line.Line <= source.EndLine {
					found = true
				}
			}
		}
		if found {
			contextLines = append(contextLines, line)
		}
	}
	document.Coverage.ContextLines = contextLines
	return document
}

func validateSavedResults(plan Plan, s state) error {
	roots := 0
	ids := map[string]bool{}
	for _, w := range s.Works {
		if ids[w.ID] || w.ID == "" {
			return errors.New("saved run contains duplicate work identities")
		}
		ids[w.ID] = true
		if w.Role != "primary" && w.Role != "fallback" {
			return errors.New("saved run has an invalid provider role")
		}
		if w.ParentID == "" {
			if roots >= len(plan.Prepared.Batches) || w.Role != "primary" {
				return errors.New("saved run roots differ from the plan")
			}
			a, _ := json.Marshal(w.Prepared)
			b, _ := json.Marshal(plan.Prepared.Batches[roots])
			if !bytes.Equal(a, b) {
				return errors.New("saved run source differs from the plan")
			}
			roots++
		} else if !ids[w.ParentID] {
			return errors.New("saved work has an unknown parent")
		}
		if w.Result != nil {
			var raw string
			found := false
			for _, output := range s.Outputs {
				if output.WorkID == w.ID && output.Attempt == w.Attempts {
					raw = output.Output
					found = true
				}
			}
			if !found {
				return errors.New("saved result has no model output")
			}
			validated, e := review.Validate(w.Prepared, json.RawMessage(raw), review.Identity{RunID: plan.RunID, PassID: plan.PassID, ClusterID: w.ID, PolicyVersion: 2})
			if e != nil {
				return errors.New("saved model output is no longer valid")
			}
			a, _ := json.Marshal(validated)
			b, _ := json.Marshal(w.Result)
			if !bytes.Equal(a, b) {
				return errors.New("saved result differs from source validation")
			}
		}
	}
	if roots != len(plan.Prepared.Batches) {
		return errors.New("saved run is missing planned source batches")
	}
	return nil
}

func allowed(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func finish(plan Plan, s state) Result {
	results := []review.BatchResult{}
	limitations := append([]string{}, plan.Prepared.Limitations...)
	complete := plan.Prepared.IsComplete
	for _, w := range s.Works {
		if w.Superseded {
			continue
		}
		if w.Result != nil {
			results = append(results, *w.Result)
		} else {
			complete = false
			message := w.Failure
			if message == "" {
				message = "unfinished batch"
			}
			limitations = append(limitations, message)
		}
	}
	aggregate := review.Aggregate(results, limitations, complete)
	status := "complete"
	if !aggregate.IsComplete || !complete {
		status = "partial"
		if len(results) == 0 && len(s.Attempts) > 0 {
			status = "failed"
		}
	}
	return Result{1, "ReviewResult", Identity(), Contract(), plan.RunID, plan.PassID, status, aggregate, s.Attempts}
}
