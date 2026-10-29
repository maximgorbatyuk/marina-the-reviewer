package review

import (
	"encoding/json"
	"testing"
)

func TestMaterializeExplicitSharedDocument(t *testing.T) {
	b := sampleBatch()
	b.Input.Graph.Sha256 = "g"
	b.Input.Batch.GraphSha256 = "g"
	d := Document{BaseSha: b.Input.BaseSha, HeadSha: b.Input.HeadSha, Graph: Graph{Sha256: "g", Nodes: []GraphNode{{ID: "n1", SourceSymbolID: "s1", Path: "A.java", Name: "f", Kind: 4, Revision: Head, StartLine: 2, EndLine: 4}}, Edges: []GraphEdge{}, Limitations: []string{}}}
	full := b
	full.Input.Graph = d.Graph
	full.UserPrompt = SerializeInput(full.Input)
	b.UserPrompt = full.UserPrompt
	b.InputSha256 = Hash(b.UserPrompt)
	raw := marshal(map[string]any{"format": "shared-review-document-v1", "documentArtifactId": "00000000-0000-0000-0000-000000000001", "batch": b})
	if _, e := ImportPreparedBatch(raw, nil); e == nil {
		t.Fatal("shared batch accepted without supplied document")
	}
	got, e := ImportPreparedBatch(raw, marshal(d))
	if e != nil {
		t.Fatal(e)
	}
	if len(got.Input.Graph.Nodes) != 1 {
		t.Fatal("graph not restored")
	}
	d.HeadSha = "wrong"
	if _, e = ImportPreparedBatch(raw, marshal(d)); e == nil {
		t.Fatal("mismatched source accepted")
	}
}
func TestImportRejectsInconsistentStandaloneContract(t *testing.T) {
	base := sampleBatch()
	base.Input.Graph.Sha256 = "graph"
	base.Input.Batch.GraphSha256 = "graph"
	base.UserPrompt = SerializeInput(base.Input)
	base.InputSha256 = Hash(base.UserPrompt)
	if _, e := ImportPreparedBatch(marshal(base), nil); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*PreparedBatch){func(b *PreparedBatch) { b.Input.Purpose = 0 }, func(b *PreparedBatch) { b.Input.Batch.GraphSha256 = "other" }, func(b *PreparedBatch) { b.InputSha256 = "other" }, func(b *PreparedBatch) { b.UserPrompt = `{"checkedBlockIds":[]}`; b.InputSha256 = Hash(b.UserPrompt) }, func(b *PreparedBatch) { b.Input.Batch.Blocks[0].Head.Content = "tampered" }} {
		b := base
		b.Input.Batch.Blocks = append([]Block{}, base.Input.Batch.Blocks...)
		source := *base.Input.Batch.Blocks[0].Head
		b.Input.Batch.Blocks[0].Head = &source
		change(&b)
		if _, e := ImportPreparedBatch(marshal(b), nil); e == nil {
			t.Fatal("inconsistent contract accepted")
		}
	}
}
func TestPascalCaseModelArchive(t *testing.T) {
	raw := json.RawMessage(`{"EvidenceProtocolVersion":3,"RepairAttempt":0,"Output":{"checkedBlockIds":["b1"],"findings":[],"strengths":[],"unconfirmedDependencies":[]},"Usage":{},"FinishReason":"stop"}`)
	if _, e := Validate(sampleBatch(), raw, Identity{}); e != nil {
		t.Fatal(e)
	}
}
func TestModelArchiveRequiresCurrentProtocolVersion(t *testing.T) {
	for _, version := range []string{"0", "1", "99", "null", `"3"`} {
		raw := json.RawMessage(`{"EvidenceProtocolVersion":` + version + `,"Output":{"checkedBlockIds":[],"findings":[],"strengths":[],"unconfirmedDependencies":[]}}`)
		if _, e := ImportRawOutput(raw); e == nil {
			t.Fatalf("archive version %s was accepted", version)
		}
	}
}
