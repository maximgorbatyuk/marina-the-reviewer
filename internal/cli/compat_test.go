package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestModelArchiveRoundTripIsNotAValidatedReview(t *testing.T) {
	raw := `{"EvidenceProtocolVersion":3,"RepairAttempt":1,"Output":{"checkedBlockIds":[],"findings":[],"strengths":[],"unconfirmedDependencies":[]},"Usage":{"InputTokens":10,"OutputTokens":2,"TotalTokens":12},"FinishReason":"stop","RejectedStrengthWarnings":["preserved"]}`
	var native, errOut bytes.Buffer
	if code := Run([]string{"compat", "import", "--input", "-"}, bytes.NewBufferString(raw), &native, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var artifact marinaArtifact
	if e := json.Unmarshal(native.Bytes(), &artifact); e != nil {
		t.Fatal(e)
	}
	if artifact.Kind != "MarinaArtifact" || artifact.ArtifactType != "ReviewModelOutputArchive" {
		t.Fatalf("wrong kind: %s", native.String())
	}
	var exported bytes.Buffer
	if code := Run([]string{"compat", "export", "--input", "-", "--format", "marina-v3"}, &native, &exported, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var a, b any
	json.Unmarshal([]byte(raw), &a)
	json.Unmarshal(exported.Bytes(), &b)
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	if !bytes.Equal(aj, bj) {
		t.Fatal("archive metadata changed")
	}
}

func TestUnknownArchiveProtocolIsRejected(t *testing.T) {
	var out, errs bytes.Buffer
	if code := Run([]string{"compat", "import", "--input", "-"}, bytes.NewBufferString(`{"EvidenceProtocolVersion":99,"Output":{}}`), &out, &errs); code != 5 {
		t.Fatalf("exit %d", code)
	}
}

func TestAuditStrippedResultRemainsAnUnvalidatedArtifact(t *testing.T) {
	raw := json.RawMessage(`{"Findings":[],"Strengths":[],"CheckedBlockIds":[],"IsComplete":true,"ValidationPolicyVersion":0}`)
	a, ok, e := importArtifact(raw)
	if e != nil || !ok || a.ArtifactType != "SignificantReviewBatchResult" {
		t.Fatalf("valid policy0 DTO rejected: %v", e)
	}
	if a.Kind != "MarinaArtifact" {
		t.Fatal("import claimed fresh validation")
	}
}
