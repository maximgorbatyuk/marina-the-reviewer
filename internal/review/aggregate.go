package review

import (
	"slices"
	"strings"
)

func makeWitness(input BatchInput, blocks map[string]Block, primary Block, evidence []Evidence, fp, rule, invariant string) FindingWitness {
	w := FindingWitness{Fingerprint: fp, RuleID: rule, InvariantID: invariant, Anchors: []ChangedEvidenceAnchor{}, BaseSha: input.BaseSha, HeadSha: input.HeadSha}
	safe := true
	primaryBefore, primaryAfter, onlyPrimaryHead := false, false, true
	for _, e := range evidence {
		if e.BlockID == primary.ID {
			if e.Revision == "base" {
				primaryBefore = true
			} else {
				primaryAfter = true
			}
		} else if e.Revision == "head" {
			onlyPrimaryHead = false
		}
		b := blocks[e.BlockID]
		s := b.Base
		sha := input.BaseSha
		if e.Revision == "head" {
			s = b.Head
			sha = input.HeadSha
		}
		if !s.IsComplete || !strings.HasSuffix(strings.ToLower(s.Path), ".java") || lexicalRisk(s.Content) || strings.Contains(s.Content, "/*") {
			safe = false
			continue
		}
		ls := lines(s.Content)
		for _, ch := range b.ChangedLines {
			if ch.Revision != s.Revision || ch.Line < e.StartLine || ch.Line > e.EndLine {
				continue
			}
			text := strings.TrimSpace(ls[ch.Line-s.StartLine])
			if text == "" || strings.Trim(text, "{};") == "" || strings.HasPrefix(text, "//") {
				continue
			}
			if b.Kind == "method" && ch.Line == s.SymbolStartLine && strings.HasSuffix(text, "{") && strings.Contains(text, "(") && strings.HasSuffix(strings.TrimSpace(strings.TrimSuffix(text, "{")), ")") && !strings.ContainsAny(text, ";=") && strings.Count(text, "{") == 1 {
				continue
			}
			anchor := ChangedEvidenceAnchor{s.Path, e.Revision, sha, ch.Line, Hash(text), e.BlockID == primary.ID}
			if !slices.Contains(w.Anchors, anchor) {
				w.Anchors = append(w.Anchors, anchor)
			}
		}
	}
	w.HasPairedPrimaryEvidence = safe && primary.BaseExists && primaryBefore && primaryAfter
	w.IsNewHelperOnly = safe && !primary.BaseExists && primary.Kind == "method" && primaryAfter && onlyPrimaryHead
	return w
}
func duplicateHelpers(findings []Finding, witnesses []FindingWitness) []DuplicateRejection {
	present := map[string]bool{}
	for _, f := range findings {
		present[f.Fingerprint] = true
	}
	out := []DuplicateRejection{}
	seen := map[string]bool{}
	for _, helper := range witnesses {
		if !present[helper.Fingerprint] || !helper.IsNewHelperOnly || seen[helper.Fingerprint] {
			continue
		}
		seen[helper.Fingerprint] = true
		heads := []ChangedEvidenceAnchor{}
		for _, a := range helper.Anchors {
			if a.Revision == "head" {
				heads = append(heads, a)
			}
		}
		if len(heads) == 0 {
			continue
		}
		eligible := []FindingWitness{}
		for _, p := range witnesses {
			if present[p.Fingerprint] && p.HasPairedPrimaryEvidence && p.Fingerprint != helper.Fingerprint && p.RuleID == helper.RuleID && p.InvariantID == helper.InvariantID && p.BaseSha != "" && p.HeadSha != "" && p.BaseSha == helper.BaseSha && p.HeadSha == helper.HeadSha {
				eligible = append(eligible, p)
			}
		}
		slices.SortFunc(eligible, func(a, b FindingWitness) int { return ordinalCompare(a.Fingerprint, b.Fingerprint) })
		for _, p := range eligible {
			shared := []ChangedEvidenceAnchor{}
			for _, a := range p.Anchors {
				if a.IsPrimary && a.Revision == "base" && slices.ContainsFunc(helper.Anchors, func(b ChangedEvidenceAnchor) bool {
					return b.Revision == "base" && a.FilePath == b.FilePath && a.RevisionSha == b.RevisionSha && a.Line == b.Line && a.SourceSha256 == b.SourceSha256
				}) {
					shared = append(shared, a)
				}
			}
			if len(shared) > 0 && !slices.ContainsFunc(heads, func(a ChangedEvidenceAnchor) bool {
				return !slices.ContainsFunc(shared, func(b ChangedEvidenceAnchor) bool { return a.SourceSha256 == b.SourceSha256 })
			}) {
				out = append(out, DuplicateRejection{helper.Fingerprint, p.Fingerprint, "MATCHED_NEW_HELPER_WITNESS", shared})
				break
			}
		}
	}
	return out
}
func Aggregate(results []BatchResult, limitations []string, coverageComplete bool) BatchResult {
	r := emptyResult()
	r.IsComplete = coverageComplete
	findingIndex, strengthIndex := map[string]int{}, map[string]int{}
	for _, x := range results {
		r.IsComplete = r.IsComplete && x.IsComplete
		r.Limitations = append(r.Limitations, x.Limitations...)
		r.CheckedBlockIDs = append(r.CheckedBlockIDs, x.CheckedBlockIDs...)
		r.UnconfirmedDependencies = append(r.UnconfirmedDependencies, x.UnconfirmedDependencies...)
		r.RejectedItemCount += x.RejectedItemCount
		r.ValidationPolicyVersion = max(r.ValidationPolicyVersion, x.ValidationPolicyVersion)
		r.Candidates = append(r.Candidates, x.Candidates...)
		r.Claims = append(r.Claims, x.Claims...)
		r.EvidenceCorrections = append(r.EvidenceCorrections, x.EvidenceCorrections...)
		r.DependencyRejections = append(r.DependencyRejections, x.DependencyRejections...)
		r.FindingWitnesses = append(r.FindingWitnesses, x.FindingWitnesses...)
		r.DuplicateRejections = append(r.DuplicateRejections, x.DuplicateRejections...)
		for _, f := range x.Findings {
			i, ok := findingIndex[f.Fingerprint]
			if !ok {
				findingIndex[f.Fingerprint] = len(r.Findings)
				r.Findings = append(r.Findings, f)
			} else if f.Confidence > r.Findings[i].Confidence {
				r.Findings[i] = f
			}
		}
		for _, s := range x.Strengths {
			i, ok := strengthIndex[s.Fingerprint]
			if !ok {
				strengthIndex[s.Fingerprint] = len(r.Strengths)
				r.Strengths = append(r.Strengths, s)
			} else if s.Confidence > r.Strengths[i].Confidence {
				r.Strengths[i] = s
			}
		}
	}
	contradictory := map[string]bool{}
	for fp := range findingIndex {
		if _, ok := strengthIndex[fp]; ok {
			contradictory[fp] = true
		}
	}
	r.Claims = distinct(r.Claims)
	for _, f := range r.Claims {
		if f.IsStrength {
			continue
		}
		for _, s := range r.Claims {
			if s.IsStrength && s.InvariantID == f.InvariantID && s.FilePath == f.FilePath && s.StartLine <= f.EndLine && f.StartLine <= s.EndLine {
				contradictory[f.Fingerprint] = true
				contradictory[s.Fingerprint] = true
			}
		}
	}
	eligible := []Finding{}
	for _, f := range r.Findings {
		if !contradictory[f.Fingerprint] {
			eligible = append(eligible, f)
		}
	}
	duplicates := duplicateHelpers(eligible, r.FindingWitnesses)
	duplicateByFP := map[string]DuplicateRejection{}
	for _, d := range duplicates {
		duplicateByFP[d.RejectedFingerprint] = d
	}
	r.DuplicateRejections = append(r.DuplicateRejections, duplicates...)
	for _, f := range r.Findings {
		if contradictory[f.Fingerprint] {
			r.RejectedItemCount++
		}
	}
	for _, s := range r.Strengths {
		if contradictory[s.Fingerprint] {
			r.RejectedItemCount++
		}
	}
	r.RejectedItemCount += len(duplicateByFP)
	r.Findings = slices.DeleteFunc(r.Findings, func(f Finding) bool {
		_, dup := duplicateByFP[f.Fingerprint]
		return contradictory[f.Fingerprint] || dup
	})
	r.Strengths = slices.DeleteFunc(r.Strengths, func(s Strength) bool { return contradictory[s.Fingerprint] })
	r.Limitations = append(r.Limitations, limitations...)
	keys := []string{}
	for fp := range contradictory {
		keys = append(keys, fp)
	}
	slices.SortFunc(keys, ordinalCompare)
	for _, fp := range keys {
		r.Limitations = append(r.Limitations, "CONTRADICTORY_OUTCOME: "+fp)
	}
	retained := map[string]string{}
	for _, f := range r.Findings {
		retained["findings|"+f.Fingerprint] = f.ReviewChunkID
	}
	for _, s := range r.Strengths {
		retained["strengths|"+s.Fingerprint] = s.ReviewChunkID
	}
	slices.SortStableFunc(r.Candidates, func(a, b CandidateDecision) int {
		priority := func(c CandidateDecision) int {
			if c.Fingerprint != nil && retained[c.ResultKind+"|"+*c.Fingerprint] != c.ReviewClusterID {
				return 1
			}
			return 0
		}
		return priority(a) - priority(b)
	})
	seen := map[string]bool{}
	for i, c := range r.Candidates {
		if c.State != "Accepted" || c.Fingerprint == nil {
			continue
		}
		fp := *c.Fingerprint
		if contradictory[fp] {
			c.State = "Suppressed"
			c.ReasonCode = "CONTRADICTORY_OUTCOME"
			c.Reason = "The finding and strength make conflicting claims about the same change."
		} else if d, ok := duplicateByFP[fp]; ok {
			c.State = "Suppressed"
			c.ReasonCode = "DUPLICATE"
			c.Reason = d.Reason
		} else if key := c.ResultKind + "|" + fp; seen[key] {
			c.State = "Suppressed"
			c.ReasonCode = "DUPLICATE"
			c.Reason = "Another candidate with the same evidence was retained."
		} else {
			seen[key] = true
		}
		r.Candidates[i] = c
	}
	r.Limitations = unique(r.Limitations)
	r.CheckedBlockIDs = unique(r.CheckedBlockIDs)
	slices.SortFunc(r.CheckedBlockIDs, ordinalCompare)
	r.UnconfirmedDependencies = unique(r.UnconfirmedDependencies)
	r.EvidenceCorrections = distinct(r.EvidenceCorrections)
	r.DependencyRejections = distinct(r.DependencyRejections)
	return r
}
func distinct[T comparable](values []T) []T {
	out := []T{}
	seen := map[T]bool{}
	for _, v := range values {
		if !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	return out
}
