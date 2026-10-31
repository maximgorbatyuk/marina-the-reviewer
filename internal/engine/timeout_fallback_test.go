package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

func TestStreamingTimeoutRetriesThenReplansForConfiguredFallback(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "configured", true: "disabled"}[disabled], func(t *testing.T) {
			var primaryCalls, backupCalls atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				primaryCalls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"continued analysis\",\"content\":\"partial\"}}]}\n\n"))
				w.(http.Flusher).Flush()
				<-request.Context().Done()
			}))
			defer primary.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				backupCalls.Add(1)
				var body struct {
					Model    string `json:"model"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Model != "backup-model" || len(body.Messages) != 2 {
					t.Errorf("invalid fallback request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var input struct {
					Batch struct {
						Blocks []struct {
							ID string `json:"id"`
						} `json:"blocks"`
					} `json:"batch"`
				}
				if err := json.Unmarshal([]byte(body.Messages[1].Content), &input); err != nil {
					t.Error(err)
					return
				}
				ids := make([]string, 0, len(input.Batch.Blocks))
				for _, block := range input.Batch.Blocks {
					ids = append(ids, block.ID)
				}
				recoveryResponse(w, recoveryOutput(ids, ""))
			}))
			defer backup.Close()
			cfg, _ := routeFor(t, primary.URL, backup.URL)
			profile := cfg.Profiles["primary"]
			profile.TimeoutSeconds, profile.MaximumAttempts, profile.MaximumOutputTokens = 1, 2, 1024
			profile.ContextLength = 262144
			profile.Stream = true
			cfg.Profiles["primary"] = profile
			profile = cfg.Profiles["backup"]
			profile.ContextLength, profile.MaximumOutputTokens = 65536, 512
			cfg.Profiles["backup"] = profile
			cfg.Routing.FallbackOn = []string{"timeout"}
			route, err := config.ResolveRouting(cfg, config.Overrides{NoFallback: disabled})
			if err != nil {
				t.Fatal(err)
			}
			source := recoveryDocument()
			for i := range source.Blocks {
				source.Blocks[i].Head.Content = strings.Repeat("source ", 1500)
			}
			plan, err := BuildDocument(source, route, "", nil)
			if err != nil || len(plan.Prepared.Batches) != 1 {
				t.Fatalf("fixture does not produce one primary batch: %v", err)
			}
			directory := filepath.Join(t.TempDir(), "run")
			result, err := Run(context.Background(), plan, cfg, directory)
			if primaryCalls.Load() != 2 {
				t.Fatalf("primary did not respect its retry limit: %d", primaryCalls.Load())
			}
			if disabled {
				if err == nil || result.Status != "failed" || backupCalls.Load() != 0 {
					t.Fatalf("disabled fallback was used or timeout lost: %s %v calls=%d", result.Status, err, backupCalls.Load())
				}
				return
			}
			if err != nil || result.Status != "complete" || len(result.Result.CheckedBlockIDs) != len(plan.Document.Blocks) || backupCalls.Load() < 2 {
				t.Fatalf("fallback lost coverage or smaller-context replan: %s %v blocks=%d calls=%d", result.Status, err, len(result.Result.CheckedBlockIDs), backupCalls.Load())
			}
			archive, err := ExportArchive(directory)
			if err != nil || len(archive.Works) < 3 || !archive.Works[0].Superseded || len(archive.Attempts) != 2+int(backupCalls.Load()) {
				t.Fatalf("fallback did not preserve attempts and lineage: %v", err)
			}
			for _, attempt := range archive.Attempts[:2] {
				if attempt.Code != "timeout" || attempt.OutcomeUnknown || attempt.Diagnostic == nil || !strings.Contains(attempt.Diagnostic.Message, "terminal finish reason absent") || !attempt.ResponsePayloadTruncated || attempt.RawOutput != "partial" {
					t.Fatal("timeout archive lost its classification or safe response diagnostic")
				}
			}
			for _, child := range archive.Works[1:] {
				if child.Role != "fallback" || child.ParentWorkID != archive.Works[0].WorkID || child.Result == nil || child.Prepared.Budget.ContextLength != 65536 || child.Prepared.MaximumOutputTokens != 512 {
					t.Fatal("fallback changed its provider budget or lost a replacement result")
				}
			}
			before := backupCalls.Load()
			if _, err = Run(context.Background(), plan, cfg, directory); err != nil || primaryCalls.Load() != 2 || backupCalls.Load() != before {
				t.Fatalf("resume repeated completed inference: %v", err)
			}
		})
	}
}
