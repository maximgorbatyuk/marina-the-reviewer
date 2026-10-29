package review

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v: %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func TestPreparePinsObjectsAndDoesNotChangeWorktree(t *testing.T) {
	d := t.TempDir()
	gitTest(t, d, "init", "-q")
	p := filepath.Join(d, "a file\nname.java")
	os.WriteFile(p, []byte("class A {\n int f() { return 1; }\n}\n"), 0600)
	gitTest(t, d, "add", ".")
	gitTest(t, d, "commit", "-qm", "base")
	base := gitTest(t, d, "rev-parse", "HEAD")
	os.WriteFile(p, []byte("class A {\n int f() { return 2; }\n}\n"), 0600)
	gitTest(t, d, "commit", "-qam", "head")
	head := gitTest(t, d, "rev-parse", "HEAD")
	os.WriteFile(p, []byte("dirty\n"), 0600)
	before := gitTest(t, d, "status", "--porcelain=v1", "-z")
	s, e := Prepare(context.Background(), PrepareOptions{Repository: d, Base: base, Head: head, ExactBase: true})
	if e != nil {
		t.Fatal(e)
	}
	if s.BaseSha != base || s.HeadSha != head || len(s.Files) != 1 || !strings.Contains(s.Files[0].Head.Content, "return 2") {
		t.Fatalf("bad snapshot: %+v", s)
	}
	if got := gitTest(t, d, "status", "--porcelain=v1", "-z"); got != before {
		t.Fatal("prepare changed worktree")
	}
	if b, _ := os.ReadFile(p); string(b) != "dirty\n" {
		t.Fatal("prepare read or changed dirty content")
	}
	again, e := Prepare(context.Background(), PrepareOptions{Repository: d, Base: base, Head: head, ExactBase: true})
	if e != nil || s.ManifestSha256 != again.ManifestSha256 {
		t.Fatal("snapshot hash is unstable")
	}
}
func sampleBatch() PreparedBatch {
	b := Block{ID: "b1", OriginalBlockID: "b1", FilePath: "A.java", Kind: "method", BaseExists: true, HeadExists: true, PartIndex: 1, PartCount: 1, SymbolIDs: []string{}, Base: &CodeSlice{Path: "A.java", Revision: Base, StartLine: 2, EndLine: 4, SymbolStartLine: 2, SymbolEndLine: 4, IsComplete: true, Content: " int f() {\n  return 1;\n }"}, Head: &CodeSlice{Path: "A.java", Revision: Head, StartLine: 2, EndLine: 4, SymbolStartLine: 2, SymbolEndLine: 4, IsComplete: true, Content: " int f() {\n  return 2;\n }"}, ChangedLines: []ChangedLine{{Base, 3}, {Head, 3}}}
	return PreparedBatch{Input: BatchInput{Version: 3, Purpose: 3, BaseSha: strings.Repeat("a", 40), HeadSha: strings.Repeat("b", 40), Graph: Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Limitations: []string{}}, Batch: Batch{Index: 1, Count: 1, Blocks: []Block{b}, IsWholeReview: true}, Limitations: []string{}}}
}
func findingRaw(quote string, line int) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"checkedBlockIds": []string{"b1"}, "findings": []any{map[string]any{"blockId": "b1", "ruleId": "BEHAVIOR", "invariantId": " result ", "severity": "Error", "title": "Incorrect result", "message": "The changed result breaks callers.", "confidence": 0.9, "evidence": []any{map[string]any{"blockId": "b1", "revision": "head", "startLine": line, "endLine": line, "quote": quote}}, "requiredSymbolIds": []string{}, "graphEdgeIds": []string{}}}, "strengths": []any{}, "unconfirmedDependencies": []string{}})
	return b
}
func TestValidateEvidenceAndCandidateAudit(t *testing.T) {
	b := sampleBatch()
	id := Identity{RunID: "00112233-4455-6677-8899-aabbccddeeff", PassID: "ffeeddcc-bbaa-9988-7766-554433221100", ClusterID: "00000000-0000-0000-0000-000000000001"}
	r, e := Validate(b, findingRaw("  return 2;", 2), id)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Findings) != 1 || len(r.EvidenceCorrections) != 1 || r.Findings[0].NewLine == nil || *r.Findings[0].NewLine != 3 {
		t.Fatalf("valid evidence lost: %+v", r)
	}
	r, e = Validate(b, findingRaw("return 99;", 3), id)
	if e != nil || len(r.Findings) != 0 || r.RejectedItemCount != 1 || r.Candidates[0].ReasonCode != "SOURCE_QUOTE_MISMATCH" {
		t.Fatalf("fabricated evidence accepted: %+v %v", r, e)
	}
	if _, e = Validate(b, json.RawMessage(`{"checkedBlockIds":["b1","b1"],"findings":[],"strengths":[],"unconfirmedDependencies":[]}`), id); e == nil {
		t.Fatal("duplicate coverage accepted")
	}
}
func TestPlanSplitsWithoutLosingPrimarySource(t *testing.T) {
	b := sampleBatch()
	s := strings.Repeat(" int value = 1;\n", 1000)
	b.Input.Batch.Blocks[0].Head.Content = strings.TrimSuffix(s, "\n")
	b.Input.Batch.Blocks[0].Head.StartLine = 1
	b.Input.Batch.Blocks[0].Head.EndLine = 1000
	b.Input.Batch.Blocks[0].Head.SymbolStartLine = 1
	b.Input.Batch.Blocks[0].Head.SymbolEndLine = 1000
	b.Input.Batch.Blocks[0].Base = nil
	b.Input.Batch.Blocks[0].BaseExists = false
	b.Input.Batch.Blocks[0].ChangedLines = []ChangedLine{{Head, 3}}
	d := Document{Version: 3, Blocks: b.Input.Batch.Blocks, Graph: b.Input.Graph, Coverage: Coverage{IsComplete: true}}
	p, e := Plan(d, PlanOptions{SystemPrompt: "review", ContextLength: 9000, OutputTokens: 1000, SafetyTokens: 100})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Batches) < 2 {
		t.Fatalf("no split: %d", len(p.Batches))
	}
	var content []string
	var changed []ChangedLine
	for _, x := range p.Batches {
		if x.EstimatedInputTokens+x.MaximumOutputTokens+100 > 9000 {
			t.Fatal("request exceeds budget")
		}
		for _, block := range x.Input.Batch.Blocks {
			if block.Head != nil {
				content = append(content, block.Head.Content)
			}
			changed = append(changed, block.ChangedLines...)
		}
	}
	if strings.Join(content, "\n") != b.Input.Batch.Blocks[0].Head.Content || !reflect.DeepEqual(changed, []ChangedLine{{Head, 3}}) {
		t.Fatal("source coverage changed")
	}
}
func TestCloudEstimatorCountsUnicodeRunes(t *testing.T) {
	if got := EstimateTokens("a", "😀", json.RawMessage(`{}`)); got != 388 {
		t.Fatalf("got %d, want 388", got)
	}
}

func TestAnalyzeJavaRetainsChangedBodiesAndResolvedCalls(t *testing.T) {
	before := "package p;\nclass A {\n int f(int x) {\n  return g(x);\n }\n int g(int x) {\n  return x;\n }\n}\n"
	after := strings.Replace(before, "return g(x)", "return g(x + 1)", 1)
	s := Snapshot{Version: 1, IsComplete: true, Files: []FileSnapshot{{OldPath: "A.java", NewPath: "A.java", Operation: "M", Base: &SourceSnapshot{Path: "A.java", Revision: Base, Content: before}, Head: &SourceSnapshot{Path: "A.java", Revision: Head, Content: after}, ChangedLines: []ChangedLine{{Base, 4}, {Head, 4}}}}}
	d, e := Analyze(s)
	if e != nil {
		t.Fatal(e)
	}
	if !d.Coverage.IsComplete || len(d.Blocks) != 1 || d.Blocks[0].Kind != "method" || d.Blocks[0].Head.StartLine != 3 || d.Blocks[0].Head.EndLine != 5 {
		t.Fatalf("bad Java selection: %+v", d)
	}
	if !slicesContainCall(d.Graph, "g") {
		t.Fatal("local call was not resolved")
	}
}
func slicesContainCall(g Graph, name string) bool {
	for _, e := range g.Edges {
		if e.Kind == "call" && e.InvokedName != nil && *e.InvokedName == name && e.Resolution == 0 && len(e.TargetIDs) == 1 {
			return true
		}
	}
	return false
}
func TestSplitDoesNotPairUnprovenRevisionRanges(t *testing.T) {
	b := sampleBatch().Input.Batch.Blocks[0]
	left, right, ok := splitBlock(b)
	if !ok {
		t.Fatal("block did not split")
	}
	if left.Base != nil && left.Head != nil || right.Base != nil && right.Head != nil {
		t.Fatal("independent cuts implied a before/after alignment")
	}
	if e := verifyCoverage([]Block{b}, [][]Block{{left}, {right}}); e != nil {
		t.Fatal(e)
	}
}
