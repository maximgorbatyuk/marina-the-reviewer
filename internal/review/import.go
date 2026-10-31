package review

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

func lookupJSON(m map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}
func importWrapper(raw json.RawMessage, kind string) (json.RawMessage, bool, error) {
	var m map[string]json.RawMessage
	if e := json.Unmarshal(raw, &m); e != nil {
		return nil, false, e
	}
	formatRaw, wrapped := lookupJSON(m, "format")
	if !wrapped {
		return raw, false, nil
	}
	var format string
	if e := json.Unmarshal(formatRaw, &format); e != nil || format != "shared-review-document-v1" {
		return nil, false, fmt.Errorf("unsupported review artifact format")
	}
	idRaw, _ := lookupJSON(m, "documentArtifactId")
	var id string
	if json.Unmarshal(idRaw, &id) != nil || id == "" || id == "00000000-0000-0000-0000-000000000000" {
		return nil, false, fmt.Errorf("shared document reference is empty")
	}
	payload, ok := lookupJSON(m, kind)
	if !ok {
		return nil, false, fmt.Errorf("shared review artifact has no %s", kind)
	}
	return payload, true, nil
}
func restoreGraph(b PreparedBatch, d Document) (PreparedBatch, error) {
	g := b.Input.Graph
	if len(g.Nodes) != 0 || len(g.Edges) != 0 || len(g.Limitations) != 0 || g.Sha256 != d.Graph.Sha256 || b.Input.Batch.GraphSha256 != d.Graph.Sha256 || b.Input.BaseSha != d.BaseSha || b.Input.StartSha != d.StartSha || b.Input.HeadSha != d.HeadSha || b.Input.ManifestSha256 != d.ManifestSha256 {
		return b, fmt.Errorf("batch does not match the supplied shared review document")
	}
	b.Input.Graph = d.Graph
	return b, nil
}

// ImportPreparedBatch validates an external prepared batch before it enters execution state.
func ImportPreparedBatch(raw, sharedDocument json.RawMessage) (PreparedBatch, error) {
	var b PreparedBatch
	payload, wrapped, e := importWrapper(raw, "batch")
	if e != nil {
		return b, e
	}
	if e = json.Unmarshal(payload, &b); e != nil {
		return b, e
	}
	if b.Input.Version != 3 {
		return b, fmt.Errorf("unsupported review input version")
	}
	if !wrapped {
		return b, validatePreparedContract(b)
	}
	if len(sharedDocument) == 0 {
		return b, fmt.Errorf("shared review document JSON is required")
	}
	var d Document
	if e = json.Unmarshal(sharedDocument, &d); e != nil {
		return b, e
	}
	b, e = restoreGraph(b, d)
	if e != nil {
		return b, e
	}
	return b, validatePreparedContract(b)
}

// ImportPreparedPlan validates an external prepared plan before it enters execution state.
func ImportPreparedPlan(raw, sharedDocument json.RawMessage) (PreparedPlan, error) {
	var p PreparedPlan
	payload, wrapped, e := importWrapper(raw, "plan")
	if e != nil {
		return p, e
	}
	if e = json.Unmarshal(payload, &p); e != nil {
		return p, e
	}
	if p.Version != 3 {
		return p, fmt.Errorf("unsupported review plan version")
	}
	if !wrapped {
		return p, validatePlanContract(p)
	}
	if len(sharedDocument) == 0 {
		return p, fmt.Errorf("shared review document JSON is required")
	}
	var d Document
	if e = json.Unmarshal(sharedDocument, &d); e != nil {
		return p, e
	}
	if p.GraphSha256 != d.Graph.Sha256 {
		return p, fmt.Errorf("plan does not match the supplied shared graph")
	}
	for i := range p.Batches {
		p.Batches[i], e = restoreGraph(p.Batches[i], d)
		if e != nil {
			return p, e
		}
	}
	return p, validatePlanContract(p)
}
func validatePreparedContract(b PreparedBatch) error {
	if e := validateBlocksAndGraph(b.Input.Batch.Blocks, b.Input.Graph); e != nil {
		return e
	}
	if b.Input.Version != 3 || b.Input.Purpose != ArchitectureReview {
		return fmt.Errorf("unsupported prepared review purpose or version")
	}
	if b.Input.Graph.Sha256 == "" || b.Input.Graph.Sha256 != b.Input.Batch.GraphSha256 {
		return fmt.Errorf("prepared batch graph hashes do not match")
	}
	if b.UserPrompt == "" || b.InputSha256 != Hash(b.UserPrompt) {
		return fmt.Errorf("prepared batch input hash does not match the user prompt")
	}
	var actual, expected any
	if json.Unmarshal([]byte(b.UserPrompt), &actual) != nil || json.Unmarshal([]byte(SerializeInput(b.Input)), &expected) != nil || !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("prepared batch user prompt does not match its input")
	}
	return nil
}
func validatePlanContract(p PreparedPlan) error {
	if p.Version != 3 || p.Purpose != ArchitectureReview {
		return fmt.Errorf("unsupported prepared review purpose or version")
	}
	if p.GraphSha256 == "" {
		return fmt.Errorf("prepared review graph hash is empty")
	}
	for _, b := range p.Batches {
		if b.Input.Graph.Sha256 != p.GraphSha256 {
			return fmt.Errorf("prepared plan and batch graph hashes do not match")
		}
		if e := validatePreparedContract(b); e != nil {
			return e
		}
	}
	return nil
}
