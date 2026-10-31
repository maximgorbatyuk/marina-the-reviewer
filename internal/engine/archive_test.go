package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func TestExecutionArchiveKeepsRejectedTextAndUsageIdentity(t *testing.T) {
	const secret = "archive-private-token"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"broken":"\u0061rchive-private-token`}, "finish_reason": "length"}}, "usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}}); err != nil {
				t.Error(err)
			}
			return
		}
		okModel(w)
	}))
	defer server.Close()
	cfg, route := routeFor(t, server.URL, "")
	route.Primary.Profile.Provider = config.ProviderGroq
	route.RuntimeSecrets = map[string]string{route.Primary.Name: secret}
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "run")
	statistics := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
	recorder := usage.NewRecorder(statistics)
	result, err := Run(usage.WithRecorder(context.Background(), recorder), plan, cfg, directory)
	if err != nil || result.Status != "complete" {
		t.Fatalf("run: %v", err)
	}
	if err = recorder.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := ExportArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Kind != "ReviewExecutionArchive" || archive.SchemaVersion != 1 || archive.RunID != plan.RunID || archive.PassID != plan.PassID || archive.Status != "complete" || len(archive.Attempts) != 2 || len(archive.Works) != 1 {
		t.Fatalf("archive: %+v", archive)
	}
	first, correction := archive.Attempts[0], archive.Attempts[1]
	if first.RequestID == "" || first.RequestID == correction.RequestID || first.RawOutput == "" || !strings.Contains(first.RawOutput, "broken") || first.Code != "output_truncated" || correction.Phase != "correction" || !strings.Contains(correction.UserPrompt, "broken") || first.OutcomeUnknown || first.RequestPayload == "" || first.Endpoint == "" || first.ResponsePayload == "" {
		t.Fatalf("attempts: %+v", archive.Attempts)
	}
	var request map[string]json.RawMessage
	if json.Unmarshal([]byte(first.RequestPayload), &request) != nil || len(request["messages"]) == 0 || len(request["response_format"]) == 0 || len(request["max_tokens"]) == 0 {
		t.Fatal("archive did not capture the actual provider request")
	}
	encoded, _ := json.Marshal(archive)
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), `\u0061rchive-private-token`) || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatal("archive did not retain safe rejected output")
	}
	stateBytes, _ := os.ReadFile(filepath.Join(directory, "state.json"))
	if strings.Contains(string(stateBytes), secret) {
		t.Fatal("credential entered private state")
	}
	native, _ := json.Marshal(result)
	if strings.Contains(string(native), "requestId") || strings.Contains(string(native), "rawOutput") {
		t.Fatal("native result contract changed")
	}
	snapshot, err := (usage.Store{Path: statistics}).Snapshot(time.Now().UTC())
	if err != nil || len(snapshot.Events) != 2 {
		t.Fatalf("statistics: %v", err)
	}
	ids := map[string]bool{}
	for _, event := range snapshot.Events {
		if event.ProviderKind != "groq" {
			t.Fatal("review or correction lost the selected provider kind")
		}
		ids[event.ID] = true
	}
	if !ids[first.RequestID] || !ids[correction.RequestID] {
		t.Fatal("archive request IDs differ from usage")
	}
	if _, err = Run(context.Background(), plan, cfg, directory); err != nil {
		t.Fatal(err)
	}
	again, err := ExportArchive(directory)
	if err != nil || calls.Load() != 2 || again.Attempts[0].RequestID != first.RequestID {
		t.Fatal("resume changed the request identity or repeated inference")
	}
}

func TestExecutionArchiveMasksTokenAfterBackslash(t *testing.T) {
	const secret = "token-private-canary"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		output, _ := json.Marshal(map[string]any{"checkedBlockIds": []string{"b1"}, "findings": []any{}, "strengths": []any{}, "unconfirmedDependencies": []string{`\` + secret}})
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(output)}, "finish_reason": "stop"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cfg, route := routeFor(t, server.URL, "")
	route.RuntimeSecrets = map[string]string{route.Primary.Name: secret}
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "run")
	if _, err = Run(context.Background(), plan, cfg, directory); err != nil {
		t.Fatal(err)
	}
	archive, err := ExportArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(archive)
	if err != nil || strings.Contains(string(encoded), secret) || !strings.Contains(archive.Attempts[0].RawOutput, "[REDACTED]") || !strings.Contains(archive.Attempts[0].ResponsePayload, "[REDACTED]") {
		t.Fatal("archive did not mask the provider token")
	}
	saved, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil || strings.Contains(string(saved), secret) {
		t.Fatal("provider token entered saved state")
	}
}

func TestExecutionArchiveExportsInterruptedRequestWithoutInference(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); okModel(w) }))
	defer server.Close()
	cfg, route := routeFor(t, server.URL, "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "run")
	encoded, _ := json.Marshal(plan)
	store, err := runstore.Open(directory, review.Hash(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runstore.AtomicJSON(filepath.Join(directory, "plan.json"), plan); err != nil {
		t.Fatal(err)
	}
	workID, requestID := UUID(), UUID()
	started := time.Now().UTC()
	saved := state{Works: []work{{ID: workID, Role: "primary", Prepared: plan.Prepared.Batches[0], Attempts: 1, InFlight: true}}, StartedAt: started,
		ArchiveAttempts: []ArchiveAttempt{{WorkID: workID, Number: 1, RequestID: requestID, Phase: "review", Code: "started", StartedAt: started, OutcomeUnknown: true}}}
	if err = store.Write(saved); err != nil {
		t.Fatal(err)
	}
	store.Close()
	archive, err := ExportArchive(directory)
	if err != nil || archive.Status != "unknown" || len(archive.Attempts) != 1 || archive.Attempts[0].RequestID != requestID || !archive.Attempts[0].OutcomeUnknown || calls.Load() != 0 {
		t.Fatalf("interrupted archive: %+v %v", archive, err)
	}
	if _, err = Run(context.Background(), plan, cfg, directory); err == nil || calls.Load() != 0 {
		t.Fatal("resume repeated an interrupted request")
	}
}

func TestExecutionArchiveRejectsCorruptOrChangedPlan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { okModel(w) }))
	defer server.Close()
	cfg, route := routeFor(t, server.URL, "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "run")
	if _, err = Run(context.Background(), plan, cfg, directory); err != nil {
		t.Fatal(err)
	}
	plan.RunID = UUID()
	if err = runstore.AtomicJSON(filepath.Join(directory, "plan.json"), plan); err != nil {
		t.Fatal(err)
	}
	if _, err = ExportArchive(directory); err == nil {
		t.Fatal("archive accepted another plan")
	}
}

func TestExecutionArchiveSizeFailurePreservesExistingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.json")
	if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := ExecutionArchive{Attempts: []ArchiveAttempt{{RawOutput: strings.Repeat("x", 128<<20)}}}
	if err := archive.Write(path); err == nil {
		t.Fatal("oversized archive was accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "retained" {
		t.Fatal("failed archive write changed the previous file")
	}
}

func TestExecutionArchiveAboveInputLimitRoundTripsStateAndExport(t *testing.T) {
	_, route := routeFor(t, "http://127.0.0.1:1", "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "run")
	encoded, _ := json.Marshal(plan)
	store, err := runstore.Open(directory, review.Hash(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runstore.AtomicJSON(filepath.Join(directory, "plan.json"), plan); err != nil {
		t.Fatal(err)
	}
	workID, started := UUID(), time.Now().UTC()
	saved := state{Works: []work{{ID: workID, Role: "primary", Prepared: plan.Prepared.Batches[0], Attempts: 1, Failure: "invalid_output: review request failed"}}, StartedAt: started,
		Attempts:        []Attempt{{WorkID: workID, Number: 1, Phase: "review", Code: "invalid_output", StartedAt: started}},
		ArchiveAttempts: []ArchiveAttempt{{WorkID: workID, Number: 1, RequestID: UUID(), Phase: "review", Code: "invalid_output", StartedAt: started, RawOutput: strings.Repeat("x", 69<<20)}}}
	if err = store.Write(saved); err != nil {
		t.Fatal(err)
	}
	store.Close()
	archive, err := ExportArchive(directory)
	if err != nil || archive.Status != "failed" || len(archive.Attempts) != 1 || len(archive.Attempts[0].RawOutput) != 69<<20 {
		t.Fatalf("large execution archive: %v", err)
	}
	path := filepath.Join(t.TempDir(), "archive.json")
	if err = archive.Write(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= runstore.MaximumDocumentBytes || info.Size() >= 128<<20 {
		t.Fatal("execution archive did not use its separate bound")
	}
	if _, err = runstore.ReadBoundedFile(path, runstore.MaximumDocumentBytes); err == nil {
		t.Fatal("input reader accepted an execution-sized document")
	}
}

func TestExecutionArchivePreservesProviderFailureBody(t *testing.T) {
	const secret = "failure-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"\u0066ailure-secret denied"}`))
	}))
	defer server.Close()
	cfg, route := routeFor(t, server.URL, "")
	route.RuntimeSecrets = map[string]string{route.Primary.Name: secret}
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "run")
	if _, err = Run(context.Background(), plan, cfg, directory); err == nil {
		t.Fatal("authentication failure was accepted")
	}
	archive, err := ExportArchive(directory)
	if err != nil || len(archive.Attempts) != 1 {
		t.Fatalf("archive: %v", err)
	}
	attempt := archive.Attempts[0]
	if attempt.Code != "authentication" || attempt.Diagnostic == nil || attempt.Diagnostic.StatusCode != 401 || attempt.Diagnostic.RetryAfterMilliseconds != 3000 || !strings.Contains(attempt.Diagnostic.RawResponse, "[REDACTED]") || strings.Contains(attempt.ResponsePayload, secret) {
		t.Fatalf("diagnostic: %+v", attempt.Diagnostic)
	}
}

func TestPrettyAndCompactSavedPlansKeepPhysicalHashAcrossResume(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-pretty", true: "large-compact"}[compact], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); okModel(w) }))
			defer server.Close()
			cfg, route := routeFor(t, server.URL, "")
			plan, err := BuildDocument(document(), route, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if compact {
				rows := make([][]int, 600000)
				row := make([]int, 16)
				for index := range rows {
					rows[index] = row
				}
				padding, err := json.Marshal(rows)
				if err != nil {
					t.Fatal(err)
				}
				plan.Route.Primary.Profile.ExtraParameters = map[string]json.RawMessage{"padding": padding}
			}
			root := t.TempDir()
			input := filepath.Join(root, "input-plan.json")
			if compact {
				if err = runstore.AtomicJSON(input, plan); err != nil {
					t.Fatal(err)
				}
			} else {
				var legacy bytes.Buffer
				encoder := json.NewEncoder(&legacy)
				encoder.SetEscapeHTML(false)
				encoder.SetIndent("", "  ")
				if err = encoder.Encode(plan); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(input, legacy.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			original, err := os.ReadFile(input)
			if err != nil || compact && bytes.Count(original, []byte{'\n'}) != 1 {
				t.Fatal("plan did not use the expected encoding")
			}
			var restored Plan
			if err = json.Unmarshal(original, &restored); err != nil {
				t.Fatal(err)
			}
			plan = restored
			encoded, _ := json.Marshal(plan)
			directory := filepath.Join(root, "execution")
			store, err := runstore.Open(directory, review.Hash(string(encoded)))
			if err != nil {
				t.Fatal(err)
			}
			workID, requestID := UUID(), UUID()
			output := json.RawMessage(`{"checkedBlockIds":["b1"],"findings":[],"strengths":[],"unconfirmedDependencies":[]}`)
			validated, err := review.Validate(plan.Prepared.Batches[0], output, review.Identity{RunID: plan.RunID, PassID: plan.PassID, ClusterID: workID, PolicyVersion: 2})
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now().UTC()
			saved := state{StartedAt: started,
				Works:           []work{{ID: workID, Role: "primary", Prepared: plan.Prepared.Batches[0], Attempts: 1, Result: &validated}},
				Outputs:         []modelOutput{{WorkID: workID, Attempt: 1, Output: string(output)}},
				Attempts:        []Attempt{{WorkID: workID, Number: 1, Phase: "review", Code: "success", StartedAt: started}},
				ArchiveAttempts: []ArchiveAttempt{{WorkID: workID, Number: 1, Phase: "review", RequestID: requestID, Code: "success", StartedAt: started}}}
			if err = store.Write(saved); err != nil {
				t.Fatal(err)
			}
			store.Close()
			for range 2 {
				if _, err = Run(context.Background(), plan, cfg, directory); err != nil {
					t.Fatal(err)
				}
				archive, err := ExportArchive(directory)
				if err != nil || archive.PlanFileSha256 != review.Hash(string(original)) {
					t.Fatalf("physical plan hash changed: %v", err)
				}
				current, err := os.ReadFile(filepath.Join(directory, "plan.json"))
				if err != nil || !bytes.Equal(current, original) || calls.Load() != 0 {
					t.Fatal("resume changed the plan bytes or repeated inference")
				}
			}
		})
	}
}
