package engine

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

func selfReviewRepository(t *testing.T) (string, string) {
	t.Helper()
	repository := t.TempDir()
	fixture := filepath.Join("..", "..", "testdata", "go-self-review")
	if err := filepath.WalkDir(fixture, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") && entry.Name() != "go.mod" {
			return nil
		}
		relative, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		target := filepath.Join(repository, relative)
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, source, 0600)
	}); err != nil {
		t.Fatal(err)
	}
	git := func(arguments ...string) string {
		t.Helper()
		args := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "user.name=Offline Test", "-c", "user.email=offline@example.invalid"}, arguments...)
		command := exec.CommandContext(t.Context(), "git", args...)
		command.Dir = repository
		data, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("temporary Git command failed: %v %s", err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	for name, marker := range map[string]string{
		"internal/review/dependency_priority.go": "return sortStableContext(ctx, additions, compareDependencyAdditions)",
		"internal/engine/engine.go":              "return BuildDocumentContext(ctx, doc, route, prompt, snapshot.Task)",
	} {
		path := filepath.Join(repository, name)
		source, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(source, []byte(marker)) {
			t.Fatalf("pinned source marker is missing: %s %v", name, err)
		}
		changed := strings.Replace(string(source), marker, marker+" // offline fixture change", 1)
		if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("commit", "-qam", "head")
	return repository, base
}

func TestGoSelfReviewPreparePlanRunValidateAndResume(t *testing.T) {
	repository, base := selfReviewRepository(t)
	snapshot, err := review.Prepare(t.Context(), review.PrepareOptions{Repository: repository, Base: base, ExactBase: true})
	if err != nil {
		t.Fatal(err)
	}
	var response string
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if err := json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": response}, "finish_reason": "stop"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cfg, route := routeFor(t, server.URL, "")
	plan, err := Build(WrapSnapshot(snapshot, nil), route, "")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Document.Coverage.IsComplete || plan.Document.SelectionVersion != 3 || len(plan.Document.Blocks) != 2 {
		t.Fatalf("own Go source was not selected completely: %+v", plan.Document.Coverage)
	}
	nodes := map[string]review.GraphNode{}
	for _, node := range plan.Document.Graph.Nodes {
		nodes[node.ID] = node
	}
	local, crossPackage := false, false
	for _, edge := range plan.Document.Graph.Edges {
		if edge.Kind != "call" || edge.Revision != review.Head || edge.Resolution != 0 {
			continue
		}
		for _, target := range edge.TargetIDs {
			from, to := nodes[edge.SourceID], nodes[target]
			local = local || from.Name == "sortDependencyAdditionsContext" && to.Name == "newDependencyPriorityIndexContext"
			crossPackage = crossPackage || from.Name == "BuildContext" && to.Name == "AnalyzeContext"
		}
	}
	if !local || !crossPackage {
		t.Fatalf("own dependencies are missing: local=%t cross-package=%t", local, crossPackage)
	}
	if len(plan.Prepared.Batches) != 1 {
		t.Fatalf("small self-review unexpectedly split into %d batches", len(plan.Prepared.Batches))
	}
	context := ""
	ids := []string{}
	for _, block := range plan.Prepared.Batches[0].Input.Batch.Blocks {
		ids = append(ids, block.ID)
		for _, source := range block.StructuralContext {
			context += source.Content
		}
	}
	if !strings.Contains(context, "func newDependencyPriorityIndexContext") || !strings.Contains(context, "func AnalyzeContext") {
		t.Fatal("own dependency bodies did not reach the actual AI batch")
	}
	primary := plan.Prepared.Batches[0].Input.Batch.Blocks[0]
	line := 0
	for _, changed := range primary.ChangedLines {
		if changed.Revision == review.Head {
			line = changed.Line
			break
		}
	}
	if primary.Head == nil || line == 0 {
		t.Fatal("own changed head source is missing")
	}
	quote := strings.Split(primary.Head.Content, "\n")[line-primary.Head.StartLine]
	finding := map[string]any{"blockId": primary.ID, "ruleId": "GO_SELF_REVIEW", "invariantId": "source-evidence", "severity": "Warning", "title": "Offline Go evidence", "message": "The offline fixture verifies a changed Go source anchor.", "confidence": 0.95,
		"evidence": []any{map[string]any{"blockId": primary.ID, "revision": "head", "startLine": line, "endLine": line, "quote": quote}}, "requiredSymbolIds": []any{}, "graphEdgeIds": []any{}}
	modelOutput, err := json.Marshal(map[string]any{"checkedBlockIds": ids, "findings": []any{finding}, "strengths": []any{}, "unconfirmedDependencies": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	response = string(modelOutput)
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	bundle, err := OpenBundle(filepath.Join(t.TempDir(), "bundle with spaces"), "go-self-review")
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	loaded, err := bundle.LoadPlan()
	if err != nil || loaded.Document.Graph.Sha256 != plan.Document.Graph.Sha256 {
		t.Fatalf("saved Go graph changed: %v", err)
	}
	directory := filepath.Join(bundle.Directory, "execution")
	result, err := Run(t.Context(), loaded, cfg, directory)
	if err != nil || result.Status != "complete" {
		t.Fatalf("offline self-review failed: %v %+v", err, result)
	}
	if len(result.Result.Findings) != 1 || result.Result.Findings[0].NewLine == nil || *result.Result.Findings[0].NewLine != line {
		t.Fatal("Go source evidence did not survive validation and aggregation")
	}
	if _, err := bundle.ExportArchive(loaded, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(t.Context(), loaded, cfg, directory); err != nil || requests.Load() != 1 {
		t.Fatalf("resume repeated the fake request: calls=%d err=%v", requests.Load(), err)
	}
}
