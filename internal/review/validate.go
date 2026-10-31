package review

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf16"
)

type modelItem struct {
	BlockID           string     `json:"blockId"`
	RuleID            string     `json:"ruleId"`
	InvariantID       string     `json:"invariantId"`
	Severity          string     `json:"severity"`
	Title             string     `json:"title"`
	Message           string     `json:"message"`
	Confidence        float64    `json:"confidence"`
	Evidence          []Evidence `json:"evidence"`
	RequiredSymbolIDs []string   `json:"requiredSymbolIds"`
	GraphEdgeIDs      []string   `json:"graphEdgeIds"`
}

func emptyResult() BatchResult {
	return BatchResult{Findings: []Finding{}, Strengths: []Strength{}, Limitations: []string{}, CheckedBlockIDs: []string{}, UnconfirmedDependencies: []string{}, Candidates: []CandidateDecision{}, Claims: []Claim{}, EvidenceCorrections: []EvidenceCorrection{}, DependencyRejections: []DependencyRejection{}, FindingWitnesses: []FindingWitness{}, DuplicateRejections: []DuplicateRejection{}, IsComplete: true}
}
func strictFields(raw json.RawMessage, required, allowed []string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if e := json.Unmarshal(raw, &m); e != nil || m == nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			return nil, fmt.Errorf("required property %s is missing", k)
		}
	}
	for k := range m {
		if !slices.Contains(allowed, k) {
			return nil, fmt.Errorf("unknown property %s", k)
		}
	}
	return m, nil
}
func stringsField(m map[string]json.RawMessage, k string) ([]string, error) {
	var out []string
	if e := json.Unmarshal(m[k], &out); e != nil || out == nil {
		return nil, fmt.Errorf("required array %s is missing", k)
	}
	for _, v := range out {
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("invalid item in %s", k)
		}
	}
	return out, nil
}

// ImportRawOutput extracts model output from a compatible archive or accepts an unwrapped output document.
func ImportRawOutput(raw json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if e := json.Unmarshal(raw, &obj); e != nil {
		return nil, e
	}
	if protocol, ok := lookupJSON(obj, "evidenceProtocolVersion"); ok {
		var version int
		if json.Unmarshal(protocol, &version) != nil || version != 3 {
			return nil, fmt.Errorf("unsupported model output evidence protocol version")
		}
		output, _ := lookupJSON(obj, "output")
		if len(output) == 0 {
			return nil, fmt.Errorf("model archive has no output")
		}
		return output, nil
	}
	return raw, nil
}

// Validate accepts findings only when their claims and evidence match the supplied prepared input.
func Validate(prepared PreparedBatch, raw json.RawMessage, id Identity) (BatchResult, error) {
	r := emptyResult()
	if e := validateBlocksAndGraph(prepared.Input.Batch.Blocks, prepared.Input.Graph); e != nil {
		return r, e
	}
	if id.PolicyVersion == 0 {
		id.PolicyVersion = 2
	}
	if id.PolicyVersion != 1 && id.PolicyVersion != 2 {
		return r, fmt.Errorf("unsupported validation policy version")
	}
	for _, p := range []*string{&id.RunID, &id.PassID, &id.ClusterID} {
		if *p == "" {
			*p = "00000000-0000-0000-0000-000000000000"
		}
	}
	if _, e := FindingID(id.RunID, id.PassID, ""); e != nil {
		return r, e
	}
	raw, e := ImportRawOutput(raw)
	if e != nil {
		return r, e
	}
	fields := []string{"checkedBlockIds", "findings", "strengths", "unconfirmedDependencies"}
	root, e := strictFields(raw, fields, fields)
	if e != nil {
		return r, e
	}
	r.CheckedBlockIDs, e = stringsField(root, "checkedBlockIds")
	if e != nil {
		return r, e
	}
	blocks := map[string]Block{}
	for _, b := range prepared.Input.Batch.Blocks {
		if _, exists := blocks[b.ID]; exists {
			return r, fmt.Errorf("duplicate prepared block ID")
		}
		blocks[b.ID] = b
	}
	checked := map[string]bool{}
	for _, v := range r.CheckedBlockIDs {
		if _, ok := blocks[v]; !ok || checked[v] {
			return r, fmt.Errorf("checkedBlockIds must include every batch block ID exactly once")
		}
		checked[v] = true
	}
	if len(checked) != len(blocks) {
		return r, fmt.Errorf("checkedBlockIds must include every batch block ID exactly once")
	}
	r.UnconfirmedDependencies, e = stringsField(root, "unconfirmedDependencies")
	if e != nil {
		return r, e
	}
	if len(r.UnconfirmedDependencies) > 20 {
		return r, fmt.Errorf("unconfirmedDependencies exceeds 20 entries")
	}
	r.Limitations = append(r.Limitations, prepared.Input.Limitations...)
	for _, v := range r.UnconfirmedDependencies {
		r.Limitations = append(r.Limitations, "Unconfirmed dependency: "+v)
	}
	projection := ProjectGraph(prepared.Input.Graph, prepared.Input.Batch)
	for _, kind := range []string{"findings", "strengths"} {
		var items []json.RawMessage
		if e := json.Unmarshal(root[kind], &items); e != nil || items == nil {
			return r, fmt.Errorf("required array %s is missing", kind)
		}
		maximum := 12
		if kind == "strengths" {
			maximum = 3
		}
		if len(items) > maximum {
			return r, fmt.Errorf("%s exceeds %d entries", kind, maximum)
		}
		for index, rawItem := range items {
			candidate := CandidateDecision{ReviewClusterID: id.ClusterID, ResultKind: kind, ItemIndex: index, Output: append(json.RawMessage(nil), rawItem...), State: "Accepted"}
			item, e := readModelItem(rawItem, kind)
			if e == nil {
				if b, ok := blocks[item.BlockID]; ok {
					p := b.FilePath
					if b.Head != nil {
						p = b.Head.Path
					} else if b.Base != nil {
						p = b.Base.Path
					}
					candidate.FilePath = &p
				}
				e = validateItem(&r, prepared, projection, blocks, item, kind, index, id, &candidate)
			}
			if e != nil {
				candidate.State = "Rejected"
				candidate.Reason = e.Error()
				candidate.ReasonCode = rejectionCode(e.Error())
				candidate.Fingerprint = nil
				r.RejectedItemCount++
				r.Limitations = append(r.Limitations, "Rejected "+kind+": "+e.Error())
			}
			r.Candidates = append(r.Candidates, candidate)
		}
	}
	r.ValidationPolicyVersion = id.PolicyVersion
	sum := Hash(string(marshal(json.RawMessage(raw))))
	r.ModelOutputSha256 = &sum
	result := Aggregate([]BatchResult{r}, nil, true)
	result.ModelOutputSha256 = &sum
	return result, nil
}
func readModelItem(raw json.RawMessage, kind string) (modelItem, error) {
	var item modelItem
	req := []string{"blockId", "ruleId", "invariantId", "title", "message", "confidence", "evidence", "requiredSymbolIds", "graphEdgeIds"}
	if kind == "findings" {
		req = append(req, "severity")
	}
	m, e := strictFields(raw, req, req)
	if e != nil {
		return item, e
	}
	if e = json.Unmarshal(raw, &item); e != nil {
		return item, fmt.Errorf("invalid result fields")
	}
	for _, v := range []struct {
		k, value string
		max      int
	}{{"blockId", item.BlockID, 0}, {"ruleId", item.RuleID, 128}, {"invariantId", item.InvariantID, 128}, {"title", item.Title, 512}, {"message", item.Message, 4000}} {
		if strings.TrimSpace(v.value) == "" || v.max > 0 && len(utf16.Encode([]rune(v.value))) > v.max {
			return item, fmt.Errorf("invalid required string %s", v.k)
		}
	}
	if bytesNull(m["confidence"]) || math.IsNaN(item.Confidence) || math.IsInf(item.Confidence, 0) || item.Confidence < 0 || item.Confidence > 1 {
		return item, fmt.Errorf("invalid confidence")
	}
	if item.RequiredSymbolIDs, e = stringsField(m, "requiredSymbolIds"); e != nil {
		return item, e
	}
	if item.GraphEdgeIDs, e = stringsField(m, "graphEdgeIds"); e != nil {
		return item, e
	}
	if len(item.Evidence) < 1 || len(item.Evidence) > 6 {
		return item, fmt.Errorf("evidence requires 1 to 6 references")
	}
	var refs []json.RawMessage
	_ = json.Unmarshal(m["evidence"], &refs)
	for _, ref := range refs {
		keys := []string{"blockId", "revision", "startLine", "endLine", "quote"}
		if _, e := strictFields(ref, keys, keys); e != nil {
			return item, e
		}
	}
	return item, nil
}
func bytesNull(v []byte) bool { return strings.TrimSpace(string(v)) == "null" }
func validateItem(r *BatchResult, p PreparedBatch, g GraphProjection, blocks map[string]Block, item modelItem, kind string, index int, id Identity, c *CandidateDecision) error {
	b, ok := blocks[item.BlockID]
	if !ok {
		return fmt.Errorf("Unknown block ID.")
	}
	for _, edge := range item.GraphEdgeIDs {
		if !slices.ContainsFunc(g.Edges, func(e GraphEdge) bool { return e.ID == edge }) {
			return fmt.Errorf("Unknown or unprovided graph edge ID.")
		}
	}
	for _, nodeID := range item.RequiredSymbolIDs {
		i := slices.IndexFunc(g.Nodes, func(n GraphNode) bool { return n.ID == nodeID })
		if i < 0 {
			return fmt.Errorf("Unknown graph node ID.")
		}
		if !hasBody(g.Nodes[i], blocks) {
			return fmt.Errorf("Unconfirmed dependency: implementation %s is not fully visible.", nodeID)
		}
	}
	evidence := append([]Evidence{}, item.Evidence...)
	hasPrimary, hasHead, hasPrimaryBase := false, false, false
	for i, original := range evidence {
		if original.BlockID == item.BlockID {
			hasPrimary = true
			if original.Revision == "base" {
				hasPrimaryBase = true
			}
		}
		corrected, correction, e := validateEvidence(original, blocks)
		if e != nil {
			return e
		}
		evidence[i] = corrected
		if corrected.Revision == "head" {
			hasHead = true
		}
		if original != corrected {
			r.EvidenceCorrections = append(r.EvidenceCorrections, EvidenceCorrection{ReviewClusterID: id.ClusterID, ResultKind: kind, ItemIndex: index, BlockID: original.BlockID, Revision: original.Revision, DeclaredStartLine: original.StartLine, DeclaredEndLine: original.EndLine, ActualStartLine: corrected.StartLine, ActualEndLine: corrected.EndLine, QuoteSha256: Hash(original.Quote), CanonicalQuoteSha256: Hash(corrected.Quote), CorrectionKind: correction})
		}
	}
	if !hasPrimary {
		return fmt.Errorf("Evidence must include the finding block.")
	}
	missing := missingDependencies(p.Input.Graph, blocks, evidence, id.ClusterID, kind, index)
	if len(missing) > 0 {
		r.DependencyRejections = append(r.DependencyRejections, missing...)
		parts := []string{}
		for _, d := range missing {
			parts = append(parts, fmt.Sprintf("%s (call %s, line %d)", d.SymbolID, d.EdgeID, d.CallLine))
		}
		dependency := "Changed MR implementations are not fully visible: " + strings.Join(parts, ", ")
		r.UnconfirmedDependencies = append(r.UnconfirmedDependencies, dependency)
		return fmt.Errorf("Unconfirmed dependency: %s", dependency)
	}
	if len(b.ChangedLines) == 0 {
		return fmt.Errorf("The result is not attached to an MR change.")
	}
	changes := append([]ChangedLine{}, b.ChangedLines...)
	inEvidence := func(ch ChangedLine) bool {
		return slices.ContainsFunc(evidence, func(e Evidence) bool {
			return e.BlockID == b.ID && e.Revision == revisionName(ch.Revision) && e.StartLine <= ch.Line && e.EndLine >= ch.Line
		})
	}
	slices.SortStableFunc(changes, func(a, b ChangedLine) int {
		if inEvidence(a) != inEvidence(b) {
			if inEvidence(a) {
				return -1
			}
			return 1
		}
		if a.Revision != b.Revision {
			if a.Revision == Head {
				return -1
			}
			return 1
		}
		return a.Line - b.Line
	})
	chosen := changes[0]
	var path string
	var oldLine, newLine *int
	if chosen.Revision == Head {
		if b.Head == nil {
			return fmt.Errorf("head change has no source")
		}
		path = b.Head.Path
		newLine = &chosen.Line
	} else {
		if b.Base == nil {
			return fmt.Errorf("base change has no source")
		}
		path = b.Base.Path
		oldLine = &chosen.Line
	}
	invariant := strings.ToUpper(strings.TrimSpace(item.InvariantID))
	fp := Fingerprint(item.RuleID, invariant, evidence, blocks)
	if !hasHead {
		return fmt.Errorf("A finding requires evidence of the affected head behavior, not only deleted code.")
	}
	if id.PolicyVersion == 1 && b.BaseExists && !hasPrimaryBase {
		return fmt.Errorf("A changed existing block requires before and after evidence.")
	}
	if kind == "findings" {
		severity := map[string]int{"Warning": 1, "Error": 2, "Critical": 3}[item.Severity]
		if severity == 0 {
			return fmt.Errorf("Invalid finding severity.")
		}
		if item.RuleID == "SOLID_SRP" && item.Confidence != 1 {
			return fmt.Errorf("SOLID_SRP requires confidence exactly 1.0.")
		}
		findingID, e := FindingID(id.RunID, id.PassID, fp)
		if e != nil {
			return e
		}
		oldPath, newPath := b.FilePath, b.FilePath
		if b.Base != nil {
			oldPath = b.Base.Path
		}
		if b.Head != nil {
			newPath = b.Head.Path
		}
		r.Findings = append(r.Findings, Finding{ID: findingID, ReviewRunID: id.RunID, ReviewPassID: id.PassID, ReviewChunkID: id.ClusterID, Purpose: p.Input.Purpose, RuleID: item.RuleID, FilePath: path, OldLine: oldLine, NewLine: newLine, Severity: severity, Title: item.Title, Message: item.Message, Evidence: canonicalJSON(evidence), Confidence: item.Confidence, Fingerprint: fp, InlinePosition: &Position{OldPath: oldPath, NewPath: newPath, OldLine: oldLine, NewLine: newLine}})
		r.FindingWitnesses = append(r.FindingWitnesses, makeWitness(p.Input, blocks, b, evidence, fp, item.RuleID, invariant))
	} else {
		r.Strengths = append(r.Strengths, Strength{ReviewRunID: id.RunID, ReviewPassID: id.PassID, ReviewChunkID: id.ClusterID, Purpose: p.Input.Purpose, RuleID: item.RuleID, FilePath: path, OldLine: oldLine, NewLine: newLine, Title: item.Title, Message: item.Message, Evidence: canonicalJSON(evidence), Confidence: item.Confidence, Fingerprint: fp})
	}
	for _, ref := range evidence {
		if ref.Revision == "head" {
			r.Claims = append(r.Claims, Claim{fp, invariant, blocks[ref.BlockID].Head.Path, ref.StartLine, ref.EndLine, kind == "strengths"})
		}
	}
	c.Fingerprint = &fp
	return nil
}
func revisionName(r Revision) string {
	if r == Head {
		return "head"
	}
	return "base"
}
func lines(v string) []string { return strings.Split(strings.ReplaceAll(v, "\r\n", "\n"), "\n") }
func lexicalRisk(s string) bool {
	return strings.Contains(s, `"""`) || strings.Contains(strings.ToLower(s), `\u`)
}
func commonIndent(ls []string) (string, bool) {
	prefix := ""
	set := false
	for _, line := range ls {
		if strings.TrimSpace(line) == "" {
			continue
		}
		p := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if !set {
			prefix = p
			set = true
		} else {
			n := 0
			for n < len(prefix) && n < len(p) && prefix[n] == p[n] {
				n++
			}
			prefix = prefix[:n]
		}
	}
	if !set {
		return "", false
	}
	out := make([]string, len(ls))
	for i, line := range ls {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, prefix) {
			return "", false
		}
		out[i] = line[len(prefix):]
	}
	return strings.Join(out, "\n"), true
}
func validateEvidence(ref Evidence, blocks map[string]Block) (Evidence, string, error) {
	b, ok := blocks[ref.BlockID]
	if !ok {
		return ref, "", fmt.Errorf("Unknown evidence block.")
	}
	var s *CodeSlice
	switch ref.Revision {
	case "base":
		s = b.Base
	case "head":
		s = b.Head
	}
	if s == nil || ref.StartLine < s.StartLine || ref.EndLine > s.EndLine || ref.EndLine < ref.StartLine || ref.StartLine < 1 || strings.TrimSpace(ref.Quote) == "" || len(utf16.Encode([]rune(ref.Quote))) > 1200 {
		return ref, "", fmt.Errorf("Evidence is outside the supplied source slice.")
	}
	ls := lines(s.Content)
	if len(ls) < s.EndLine-s.StartLine+1 {
		return ref, "", fmt.Errorf("source slice is incomplete")
	}
	actual := strings.Join(ls[ref.StartLine-s.StartLine:ref.EndLine-s.StartLine+1], "\n")
	quote := strings.TrimSpace(strings.ReplaceAll(ref.Quote, "\r\n", "\n"))
	if strings.TrimSpace(actual) == quote {
		return ref, "", nil
	}
	match := -1
	count := len(strings.Split(quote, "\n"))
	for i := 0; i+count <= s.EndLine-s.StartLine+1; i++ {
		if strings.TrimSpace(strings.Join(ls[i:i+count], "\n")) == quote {
			if match >= 0 {
				return ref, "", fmt.Errorf("Evidence quote matches multiple supplied source ranges.")
			}
			match = i
		}
	}
	correction := "Coordinates"
	if match < 0 {
		if !s.IsComplete || !strings.HasSuffix(strings.ToLower(s.Path), ".java") || lexicalRisk(s.Content) || lexicalRisk(ref.Quote) {
			return ref, "", fmt.Errorf("Evidence quote does not match the supplied source lines.")
		}
		qs := lines(ref.Quote)
		norm, ok := commonIndent(qs)
		if !ok {
			return ref, "", fmt.Errorf("Evidence quote does not match the supplied source lines.")
		}
		count = len(qs)
		for i := 0; i+count <= s.EndLine-s.StartLine+1; i++ {
			text, ok := commonIndent(ls[i : i+count])
			if ok && text == norm {
				if match >= 0 {
					return ref, "", fmt.Errorf("Evidence quote matches multiple supplied source ranges.")
				}
				match = i
			}
		}
		if match < 0 {
			return ref, "", fmt.Errorf("Evidence quote does not match the supplied source lines.")
		}
		correction = "JavaCommonIndent"
		if ref.StartLine != s.StartLine+match || ref.EndLine != s.StartLine+match+count-1 {
			correction = "JavaCommonIndentAndCoordinates"
		}
	}
	ref.StartLine = s.StartLine + match
	ref.EndLine = ref.StartLine + count - 1
	ref.Quote = strings.Join(ls[match:match+count], "\n")
	return ref, correction, nil
}
func hasBody(n GraphNode, blocks map[string]Block) bool {
	covers := func(source CodeSlice) bool {
		return source.Revision == n.Revision && source.Path == n.Path && source.IsComplete && source.StartLine <= n.StartLine && source.EndLine >= n.EndLine
	}
	for _, block := range blocks {
		for _, source := range block.StructuralContext {
			if covers(source) {
				return true
			}
		}
		source := block.Base
		if n.Revision == Head {
			source = block.Head
		}
		if source != nil && covers(*source) {
			return true
		}
	}
	return false
}
func missingDependencies(g Graph, blocks map[string]Block, ev []Evidence, cluster, kind string, index int) []DependencyRejection {
	out := []DependencyRejection{}
	nodes := map[string]GraphNode{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	for _, ref := range ev {
		if ref.Revision != "head" {
			continue
		}
		s := blocks[ref.BlockID].Head
		for _, edge := range g.Edges {
			if edge.Kind != "call" || edge.Revision != Head || edge.Resolution > 1 || edge.Line == nil || *edge.Line < ref.StartLine || *edge.Line > ref.EndLine {
				continue
			}
			source, ok := nodes[edge.SourceID]
			if !ok || source.Path != s.Path || source.Revision != Head || *edge.Line < source.StartLine || *edge.Line > source.EndLine {
				continue
			}
			for _, targetID := range edge.TargetIDs {
				target, ok := nodes[targetID]
				if !ok || target.Kind != 4 {
					continue
				}
				changed := target.Change != 0
				if target.CounterpartID != nil && nodes[*target.CounterpartID].Change != 0 {
					changed = true
				}
				if changed && !hasBody(target, blocks) {
					d := DependencyRejection{cluster, kind, index, ref.BlockID, edge.ID, targetID, *edge.Line}
					if !slices.Contains(out, d) {
						out = append(out, d)
					}
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b DependencyRejection) int {
		if x := ordinalCompare(a.EvidenceBlockID, b.EvidenceBlockID); x != 0 {
			return x
		}
		if a.CallLine != b.CallLine {
			return a.CallLine - b.CallLine
		}
		if x := ordinalCompare(a.EdgeID, b.EdgeID); x != 0 {
			return x
		}
		return ordinalCompare(a.SymbolID, b.SymbolID)
	})
	return out
}
func rejectionCode(reason string) string {
	switch {
	case strings.Contains(reason, "before and after"):
		return "BASE_EVIDENCE_REQUIRED"
	case strings.HasPrefix(reason, "Unconfirmed dependency:"):
		return "DEPENDENCY_NOT_VISIBLE"
	case strings.Contains(strings.ToLower(reason), "quote"):
		return "SOURCE_QUOTE_MISMATCH"
	case strings.Contains(strings.ToLower(reason), "head"):
		return "HEAD_EVIDENCE_REQUIRED"
	case strings.Contains(reason, "MR change"):
		return "CHANGE_ANCHOR_MISSING"
	case strings.Contains(strings.ToLower(reason), "severity"):
		return "INVALID_SEVERITY"
	case strings.Contains(strings.ToLower(reason), "confidence"):
		return "INVALID_CONFIDENCE"
	default:
		return "INVALID_EVIDENCE"
	}
}
