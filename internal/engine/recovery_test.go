package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

func recoveryDocument() review.Document {
	d := document()
	d.Blocks = nil
	for i := 0; i < 16; i++ {
		block := document().Blocks[0]
		block.ID = fmt.Sprintf("b%d", i+1)
		block.OriginalBlockID = block.ID
		block.FilePath = fmt.Sprintf("file%d.go", i+1)
		block.Head.Path = block.FilePath
		block.Head.Content = strings.Repeat("source ", 450)
		d.Blocks = append(d.Blocks, block)
	}
	return d
}

func recoveryOutput(ids []string, padding string) string {
	dependencies := []string{}
	if padding != "" {
		dependencies = append(dependencies, padding)
	}
	raw, _ := json.Marshal(map[string]any{"checkedBlockIds": ids, "findings": []any{}, "strengths": []any{}, "unconfirmedDependencies": dependencies})
	return string(raw)
}

func recoveryResponse(w http.ResponseWriter, content string) {
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}})
}

func recoveryRequest(t *testing.T, r *http.Request) ([]string, string) {
	t.Helper()
	var request struct {
		Model        string `json:"model"`
		OutputTokens int    `json:"max_tokens"`
		Messages     []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
		t.Errorf("invalid request: %v", err)
		return nil, ""
	}
	if request.Model != "primary-model" || request.OutputTokens != 1024 {
		t.Errorf("model or output reserve changed: %s/%d", request.Model, request.OutputTokens)
	}
	var input struct {
		Batch struct {
			Blocks []struct {
				ID string `json:"id"`
			} `json:"blocks"`
		} `json:"batch"`
	}
	if err := json.NewDecoder(strings.NewReader(request.Messages[1].Content)).Decode(&input); err != nil {
		t.Errorf("invalid batch input: %v", err)
	}
	ids := []string{}
	for _, block := range input.Batch.Blocks {
		ids = append(ids, block.ID)
	}
	return ids, request.Messages[1].Content
}

func recoveryPlan(t *testing.T, endpoint string, attempts int) (config.Config, Plan) {
	t.Helper()
	cfg, _ := routeFor(t, endpoint, "")
	profile := cfg.Profiles["primary"]
	profile.MaximumOutputTokens = 1024
	profile.MaximumAttempts = attempts
	cfg.Profiles["primary"] = profile
	route, err := config.ResolveRouting(cfg, config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildDocument(recoveryDocument(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Prepared.Batches) != 1 {
		t.Fatalf("expected one initial batch, got %d", len(plan.Prepared.Batches))
	}
	return cfg, plan
}

func TestCorrectionBudgetOverflowReplansWithCompleteCoverage(t *testing.T) {
	var calls atomic.Int32
	oversized := recoveryOutput([]string{"b1"}, strings.Repeat("detail ", 30000))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids, _ := recoveryRequest(t, r)
		switch calls.Add(1) {
		case 1:
			recoveryResponse(w, oversized)
		case 2:
			recoveryResponse(w, recoveryOutput([]string{}, ""))
		default:
			recoveryResponse(w, recoveryOutput(ids, ""))
		}
	}))
	defer server.Close()
	cfg, plan := recoveryPlan(t, server.URL, 1)
	root := plan.Prepared.Batches[0]
	if count := review.CountTokens(root.SystemPrompt, root.UserPrompt+oversized, review.ResponseSchema()); count.TemplatedInputTokens <= root.Budget.AvailableUserInputTokens {
		t.Fatal("fixture correction fits the original budget")
	}
	directory := filepath.Join(t.TempDir(), "run")
	result, err := Run(context.Background(), plan, cfg, directory)
	if err != nil || result.Status != "complete" || len(result.Result.CheckedBlockIDs) != len(plan.Document.Blocks) {
		t.Fatalf("recovery did not retain all blocks: status=%s checked=%d error=%v", result.Status, len(result.Result.CheckedBlockIDs), err)
	}
	archive, err := ExportArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Works) < 3 || !archive.Works[0].Superseded {
		t.Fatal("failed parent has no replacement lineage")
	}
	corrections := 0
	var expectedSchema any
	json.Unmarshal(review.ResponseSchema(), &expectedSchema)
	for _, attempt := range archive.Attempts {
		var schema any
		json.Unmarshal(attempt.Schema, &schema)
		if attempt.Provider != "primary" || attempt.Model != "primary-model" || !reflect.DeepEqual(schema, expectedSchema) {
			t.Fatal("recovery changed the provider or response schema")
		}
		if attempt.Phase == "correction" {
			corrections++
		}
	}
	if corrections != 1 {
		t.Fatalf("unsent parent correction consumed the child allowance: %d", corrections)
	}
	for _, child := range archive.Works[1:] {
		if child.ParentWorkID != archive.Works[0].WorkID || child.Prepared.ReplanDepth != 1 || child.Prepared.EstimatedInputTokens >= root.EstimatedInputTokens || child.Prepared.MaximumOutputTokens != root.MaximumOutputTokens || child.Prepared.SystemPrompt != root.SystemPrompt || child.Result == nil {
			t.Fatal("replacement changed the source contract or failed to complete")
		}
	}
	before := calls.Load()
	if _, err = Run(context.Background(), plan, cfg, directory); err != nil || calls.Load() != before {
		t.Fatalf("resume repeated completed recovery: %v", err)
	}
}

func TestCorrectionBudgetReplanRetainsDepthLimit(t *testing.T) {
	var calls atomic.Int32
	oversized := recoveryOutput([]string{}, strings.Repeat("detail ", 30000))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		recoveryResponse(w, oversized)
	}))
	defer server.Close()
	cfg, plan := recoveryPlan(t, server.URL, 1)
	directory := filepath.Join(t.TempDir(), "run")
	result, err := Run(context.Background(), plan, cfg, directory)
	if err == nil || result.Status == "complete" || len(result.Result.CheckedBlockIDs) != 0 {
		t.Fatal("exhausted recovery was reported complete")
	}
	archive, err := ExportArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	maximumDepth, failed := 0, 0
	for _, work := range archive.Works {
		maximumDepth = max(maximumDepth, work.Prepared.ReplanDepth)
		if !work.Superseded {
			failed++
			if !strings.HasPrefix(work.Failure, "recovery_budget:") {
				t.Fatalf("missing recovery failure: %s", work.Failure)
			}
		}
	}
	if maximumDepth != 3 || failed == 0 || int(calls.Load()) != len(archive.Works) {
		t.Fatalf("unexpected bounded recovery: depth=%d failed=%d calls=%d works=%d", maximumDepth, failed, calls.Load(), len(archive.Works))
	}
	before := calls.Load()
	if _, err = Run(context.Background(), plan, cfg, directory); err == nil || calls.Load() != before {
		t.Fatal("resume repeated exhausted recovery")
	}
}

func TestCorrectionBudgetReplanRetainsRequestAttemptLimit(t *testing.T) {
	var calls atomic.Int32
	oversized := recoveryOutput([]string{}, strings.Repeat("detail ", 30000))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			recoveryResponse(w, oversized)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg, plan := recoveryPlan(t, server.URL, 2)
	directory := filepath.Join(t.TempDir(), "run")
	result, err := Run(context.Background(), plan, cfg, directory)
	if err == nil || result.Status == "complete" {
		t.Fatal("exhausted request attempts were reported complete")
	}
	archive, err := ExportArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Works) < 3 || int(calls.Load()) != 1+2*(len(archive.Works)-1) {
		t.Fatalf("unexpected request bound: calls=%d works=%d", calls.Load(), len(archive.Works))
	}
	for _, child := range archive.Works[1:] {
		if child.Superseded || child.Prepared.ReplanDepth != 1 || child.Result != nil || child.Failure == "" {
			t.Fatal("transport retries escaped the replacement attempt limit")
		}
	}
	before := calls.Load()
	if _, err = Run(context.Background(), plan, cfg, directory); err == nil || calls.Load() != before {
		t.Fatal("resume repeated exhausted request attempts")
	}
}

func TestContextReplanDoesNotResetSentCorrectionAllowance(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"context_length_exceeded"}}`))
			return
		}
		recoveryResponse(w, recoveryOutput([]string{}, ""))
	}))
	defer server.Close()
	cfg, plan := recoveryPlan(t, server.URL, 1)
	directory := filepath.Join(t.TempDir(), "run")
	result, err := Run(context.Background(), plan, cfg, directory)
	if err == nil || result.Status == "complete" {
		t.Fatal("invalid child output was reported complete")
	}
	archive, err := ExportArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	corrections := 0
	for _, attempt := range archive.Attempts {
		if attempt.Phase == "correction" {
			corrections++
		}
	}
	if len(archive.Works) < 3 || corrections != 1 || int(calls.Load()) != len(archive.Works)+1 {
		t.Fatalf("sent correction allowance reset: works=%d corrections=%d calls=%d", len(archive.Works), corrections, calls.Load())
	}
}
