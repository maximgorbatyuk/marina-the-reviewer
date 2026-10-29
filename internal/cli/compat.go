package cli

import (
	"encoding/json"
	"errors"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/engine"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

type marinaArtifact struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Kind          string               `json:"kind"`
	Producer      engine.Producer      `json:"producer"`
	Compatibility engine.Compatibility `json:"compatibility"`
	ArtifactType  string               `json:"artifactType"`
	Payload       json.RawMessage      `json:"payload"`
}

func importArtifact(raw json.RawMessage) (marinaArtifact, bool, error) {
	var probe struct {
		EvidenceProtocolVersion *int            `json:"evidenceProtocolVersion"`
		Output                  json.RawMessage `json:"output"`
		RepairAttempt           int             `json:"repairAttempt"`
		Findings                json.RawMessage `json:"findings"`
		CheckedBlockIDs         json.RawMessage `json:"checkedBlockIds"`
		IsComplete              *bool           `json:"isComplete"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return marinaArtifact{}, false, errors.New("invalid Marina artifact JSON")
	}
	kind := ""
	if probe.EvidenceProtocolVersion != nil {
		if *probe.EvidenceProtocolVersion != 3 || probe.RepairAttempt < 0 || probe.RepairAttempt > 1 || len(probe.Output) == 0 || string(probe.Output) == "null" || !json.Valid(probe.Output) {
			return marinaArtifact{}, true, errors.New("unsupported model output archive")
		}
		kind = "ReviewModelOutputArchive"
	} else if probe.IsComplete != nil && len(probe.Findings) > 0 && len(probe.CheckedBlockIDs) > 0 {
		var r review.BatchResult
		if json.Unmarshal(raw, &r) != nil || r.Findings == nil || r.Strengths == nil || r.CheckedBlockIDs == nil || r.ValidationPolicyVersion < 1 || r.ValidationPolicyVersion > 2 {
			return marinaArtifact{}, true, errors.New("invalid batch result artifact")
		}
		kind = "SignificantReviewBatchResult"
	}
	if kind == "" {
		return marinaArtifact{}, false, nil
	}
	return marinaArtifact{1, "MarinaArtifact", engine.Identity(), engine.Contract(), kind, raw}, true, nil
}

func unwrapModel(raw json.RawMessage) (json.RawMessage, error) {
	var artifact marinaArtifact
	if json.Unmarshal(raw, &artifact) == nil && artifact.Kind == "MarinaArtifact" {
		if artifact.SchemaVersion != 1 || artifact.Compatibility != engine.Contract() || artifact.ArtifactType != "ReviewModelOutputArchive" {
			return nil, errors.New("expected an imported model output archive")
		}
		if _, ok, e := importArtifact(artifact.Payload); e != nil || !ok {
			return nil, errors.New("invalid imported model archive")
		}
		return artifact.Payload, nil
	}
	return raw, nil
}

func exportNative(raw json.RawMessage, batchIndex int) (any, error) {
	var header struct {
		SchemaVersion int                  `json:"schemaVersion"`
		Kind          string               `json:"kind"`
		Compatibility engine.Compatibility `json:"compatibility"`
	}
	if json.Unmarshal(raw, &header) != nil || header.SchemaVersion != 1 || header.Compatibility != engine.Contract() {
		return nil, errors.New("unsupported native document")
	}
	switch header.Kind {
	case "ReviewPlan":
		var p engine.Plan
		if json.Unmarshal(raw, &p) != nil {
			return nil, errors.New("invalid native plan")
		}
		if e := p.Validate(); e != nil {
			return nil, e
		}
		if batchIndex > 0 {
			if batchIndex > len(p.Prepared.Batches) {
				return nil, errors.New("batch index exceeds the prepared plan")
			}
			return p.Prepared.Batches[batchIndex-1], nil
		}
		return p.Prepared, nil
	case "ReviewResult":
		var r engine.Result
		if json.Unmarshal(raw, &r) != nil || r.Result.Findings == nil || r.Result.Strengths == nil || r.Result.CheckedBlockIDs == nil {
			return nil, errors.New("invalid native review result")
		}
		return r.Result, nil
	case "MarinaArtifact":
		var a marinaArtifact
		if json.Unmarshal(raw, &a) != nil {
			return nil, errors.New("invalid imported artifact")
		}
		checked, ok, e := importArtifact(a.Payload)
		if e != nil || !ok || checked.ArtifactType != a.ArtifactType {
			return nil, errors.New("invalid imported artifact payload")
		}
		return a.Payload, nil
	default:
		return nil, errors.New("export requires a native plan, result or imported Marina artifact")
	}
}
