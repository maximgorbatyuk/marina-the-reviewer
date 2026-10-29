package review

import "testing"

func TestAggregateDeduplicatesOnlySharedHelperWitnesses(t *testing.T) {
	anchor := ChangedEvidenceAnchor{FilePath: "A.java", Revision: "base", RevisionSha: "base", Line: 3, SourceSha256: "same", IsPrimary: true}
	primary := FindingWitness{Fingerprint: "primary", RuleID: "RULE", InvariantID: "GUARANTEE", HasPairedPrimaryEvidence: true, BaseSha: "base", HeadSha: "head", Anchors: []ChangedEvidenceAnchor{anchor}}
	helperBase := anchor
	helperBase.IsPrimary = false
	helperHead := ChangedEvidenceAnchor{FilePath: "Helper.java", Revision: "head", RevisionSha: "head", Line: 2, SourceSha256: "same", IsPrimary: true}
	helper := FindingWitness{Fingerprint: "helper", RuleID: "RULE", InvariantID: "GUARANTEE", IsNewHelperOnly: true, BaseSha: "base", HeadSha: "head", Anchors: []ChangedEvidenceAnchor{helperBase, helperHead}}
	r := emptyResult()
	r.Findings = []Finding{{Fingerprint: "primary"}, {Fingerprint: "helper"}}
	r.FindingWitnesses = []FindingWitness{primary, helper}
	got := Aggregate([]BatchResult{r}, nil, true)
	if len(got.Findings) != 1 || got.Findings[0].Fingerprint != "primary" || len(got.DuplicateRejections) != 1 {
		t.Fatalf("helper duplicate not removed: %+v", got)
	}
	r.FindingWitnesses[1].HeadSha = "other"
	got = Aggregate([]BatchResult{r}, nil, true)
	if len(got.Findings) != 2 {
		t.Fatal("different revisions were merged")
	}
}
