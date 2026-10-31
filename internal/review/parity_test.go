package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type parityManifest struct {
	Version int          `json:"version"`
	Cases   []parityCase `json:"cases"`
}

type parityCase struct {
	Name            string            `json:"name"`
	Snapshot        string            `json:"snapshot"`
	LegacyDocument  string            `json:"legacyDocument"`
	OutputDirectory string            `json:"outputDirectory"`
	Exceptions      []parityException `json:"exceptions"`
}

type parityException struct {
	DifferenceSha256 string `json:"differenceSha256"`
	Rule             string `json:"rule"`
	EvidenceFile     string `json:"evidenceFile"`
	EvidenceSha256   string `json:"evidenceSha256"`
}

type parityDifference struct {
	Category string `json:"category"`
	Side     string `json:"side"`
	Value    string `json:"value"`
	Count    int    `json:"count"`
	Sha256   string `json:"sha256"`
	Rule     string `json:"rule,omitempty"`
}

var parityRegressionRules = map[string]string{
	"literal-argument-arity":           "TestJavaLiteralCallsDoNotResolveZeroArgumentOverload",
	"try-resource-declarations":        "TestMRUnchangedTryResourcesDoNotCreateChangedDeclarations",
	"nested-call-discovery":            "TestMRNestedCallsRetainEverySourceInvocation",
	"interface-comment-body":           "TestMRGraphResolvesInheritedInterfaceAndFrameworkContracts/interface_comment",
	"annotated-type-member-discovery":  "TestMRTypeAnnotationArrayDoesNotHideFirstMethod",
	"callback-invocation-declarations": "TestMRLanguageNeutralFunctionsKeepRealDeclarations",
}

type parityEvidence struct {
	Version int                  `json:"version"`
	Cases   []parityEvidenceCase `json:"cases"`
}

type parityEvidenceCase struct {
	Rule                 string            `json:"rule"`
	Regression           string            `json:"regression"`
	Reason               string            `json:"reason"`
	ActualSnapshotSha256 string            `json:"actualSnapshotSha256"`
	LegacyDocumentSha256 string            `json:"legacyDocumentSha256"`
	Paths                map[string]string `json:"paths"`
	Sha256               map[string]string `json:"sha256"`
	Differences          []string          `json:"differenceSha256"`
}

func parityEvidenceMembership(path, expectedHash, snapshotHash, legacyHash string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil || expectedHash == "" || Hash(string(raw)) != expectedHash {
		return nil, fmt.Errorf("parity evidence is missing or changed")
	}
	var proof parityEvidence
	if err = json.Unmarshal(raw, &proof); err != nil || proof.Version != 2 || len(proof.Cases) == 0 || snapshotHash == "" || legacyHash == "" {
		return nil, fmt.Errorf("parity evidence has no supported case records")
	}
	verified, membership := map[string]string{}, map[string]bool{}
	matchingInputs := false
	for _, fixture := range proof.Cases {
		regression, known := parityRegressionRules[fixture.Rule]
		if !known || fixture.Regression != regression || fixture.Reason == "" || len(fixture.Paths) == 0 {
			return nil, fmt.Errorf("parity evidence has no bounded regression for %s", fixture.Rule)
		}
		if fixture.ActualSnapshotSha256 == "" || fixture.LegacyDocumentSha256 == "" {
			return nil, fmt.Errorf("parity evidence has no exact input binding")
		}
		if fixture.ActualSnapshotSha256 != snapshotHash || fixture.LegacyDocumentSha256 != legacyHash {
			continue
		}
		matchingInputs = true
		if fixture.Paths["actualSnapshot"] == "" || fixture.Paths["legacyDocument"] == "" || fixture.Sha256["actualSnapshot"] != snapshotHash || fixture.Sha256["legacyDocument"] != legacyHash {
			return nil, fmt.Errorf("parity evidence input paths do not match the current snapshot and legacy document")
		}
		for name, path := range fixture.Paths {
			hash, checked := verified[path]
			if !checked {
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					return nil, fmt.Errorf("parity source evidence is missing")
				}
				hash = Hash(string(data))
				verified[path] = hash
			}
			if fixture.Sha256[name] == "" || fixture.Sha256[name] != hash {
				return nil, fmt.Errorf("parity source evidence changed for %s", fixture.Rule)
			}
		}
		for _, difference := range fixture.Differences {
			membership[fixture.Rule+":"+difference] = true
		}
	}
	if !matchingInputs {
		return nil, fmt.Errorf("parity evidence belongs to a different snapshot or legacy document")
	}
	return membership, nil
}

func TestMarinaOfflineSemanticParity(t *testing.T) {
	path := os.Getenv("MARINADOREVIEW_PARITY_MANIFEST")
	if path == "" {
		t.Skip("Set MARINADOREVIEW_PARITY_MANIFEST to compare saved local source fixtures.")
	}
	var manifest parityManifest
	readParityJSON(t, path, &manifest)
	if manifest.Version != 1 || len(manifest.Cases) == 0 {
		t.Fatal("the parity manifest has an unsupported version or no cases")
	}
	for _, fixture := range manifest.Cases {
		t.Run(fixture.Name, func(t *testing.T) {
			snapshotBytes, err := os.ReadFile(fixture.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]json.RawMessage
			if err = json.Unmarshal(snapshotBytes, &raw); err != nil {
				t.Fatal(err)
			}
			var snapshot Snapshot
			if wrapped, found := raw["snapshot"]; found {
				if err := json.Unmarshal(wrapped, &snapshot); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = json.Unmarshal(snapshotBytes, &snapshot); err != nil {
					t.Fatal(err)
				}
			}
			var legacy Document
			legacyBytes, err := os.ReadFile(fixture.LegacyDocument)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(legacyBytes, &legacy); err != nil {
				t.Fatal(err)
			}
			snapshotHash, legacyHash := Hash(string(snapshotBytes)), Hash(string(legacyBytes))
			current, err := Analyze(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateDocument(current); err != nil {
				t.Fatal(err)
			}
			differences := compareParityDocuments(legacy, current)
			exceptions := map[string]parityException{}
			proofs := map[string]map[string]bool{}
			for _, exception := range fixture.Exceptions {
				if _, known := parityRegressionRules[exception.Rule]; !known {
					t.Fatalf("unknown parity exception rule: %s", exception.Rule)
				}
				proofKey := exception.EvidenceFile + ":" + exception.EvidenceSha256 + ":" + snapshotHash + ":" + legacyHash
				membership, checked := proofs[proofKey]
				if !checked {
					membership, err = parityEvidenceMembership(exception.EvidenceFile, exception.EvidenceSha256, snapshotHash, legacyHash)
					if err != nil {
						t.Fatal(err)
					}
					proofs[proofKey] = membership
				}
				if !membership[exception.Rule+":"+exception.DifferenceSha256] {
					t.Fatalf("parity exception has no exact bounded evidence membership: %s", exception.Rule)
				}
				if _, duplicate := exceptions[exception.DifferenceSha256]; duplicate {
					t.Fatal("the parity manifest has duplicate exception identities")
				}
				exceptions[exception.DifferenceSha256] = exception
			}
			unclassified, unused := 0, len(exceptions)
			for i := range differences {
				if exception, exists := exceptions[differences[i].Sha256]; exists {
					differences[i].Rule = exception.Rule
					unused--
				} else {
					unclassified++
				}
			}
			if err = os.MkdirAll(fixture.OutputDirectory, 0700); err != nil {
				t.Fatal(err)
			}
			writeParityJSON(t, filepath.Join(fixture.OutputDirectory, "native-document.json"), current)
			writeParityJSON(t, filepath.Join(fixture.OutputDirectory, "comparison.json"), struct {
				SelectionVersion int                `json:"selectionVersion"`
				Differences      []parityDifference `json:"differences"`
				Unclassified     int                `json:"unclassified"`
				UnusedExceptions int                `json:"unusedExceptions"`
				LegacyLimits     []string           `json:"legacyLimitations"`
				NativeLimits     []string           `json:"nativeLimitations"`
			}{current.SelectionVersion, differences, unclassified, unused, legacy.Limitations, current.Limitations})
			if unclassified != 0 || unused != 0 {
				t.Fatalf("semantic parity has %d unclassified differences and %d unused exception records; see comparison.json", unclassified, unused)
			}
		})
	}
}

func compareParityDocuments(legacy, current Document) []parityDifference {
	before, after := normalizeParityDocument(legacy), normalizeParityDocument(current)
	result := []parityDifference{}
	for _, category := range []string{"source", "node", "edge", "block", "dependency", "declaration", "coverage"} {
		counts := map[string]int{}
		for _, value := range before[category] {
			counts[value]++
		}
		for _, value := range after[category] {
			counts[value]--
		}
		values := []string{}
		for value, count := range counts {
			if count != 0 {
				values = append(values, value)
			}
		}
		slices.Sort(values)
		for _, value := range values {
			count, side := counts[value], "legacy"
			if count < 0 {
				count, side = -count, "native"
			}
			identity := canonicalJSON([]any{category, side, value, count})
			result = append(result, parityDifference{Category: category, Side: side, Value: value, Count: count, Sha256: Hash(identity)})
		}
	}
	return result
}

func normalizeParityDocument(document Document) map[string][]string {
	result := map[string][]string{}
	nodes, aliases := map[string]GraphNode{}, map[string]string{}
	for _, node := range document.Graph.Nodes {
		nodes[node.ID] = node
		aliases[node.SourceSymbolID] = node.ID
	}
	nodeKey := func(id string) string {
		if alias, found := aliases[id]; found {
			id = alias
		}
		node, found := nodes[id]
		if !found {
			return "missing:" + id
		}
		return canonicalJSON([]any{node.Path, node.Revision, node.Kind, node.Name, node.Signature, node.StartLine, node.EndLine})
	}
	sliceKey := func(source CodeSlice) string {
		return canonicalJSON([]any{source.Path, source.Revision, source.StartLine, source.EndLine, source.SymbolStartLine, source.SymbolEndLine, source.IsComplete, Hash(source.Content)})
	}
	sourceKey := func(source *CodeSlice) any {
		if source == nil {
			return nil
		}
		return sliceKey(*source)
	}
	result["source"] = []string{canonicalJSON([]any{document.ManifestSha256, document.BaseSha, document.StartSha, document.HeadSha})}
	for _, node := range document.Graph.Nodes {
		var counterpart any
		if node.CounterpartID != nil {
			counterpart = nodeKey(*node.CounterpartID)
		}
		result["node"] = append(result["node"], canonicalJSON([]any{nodeKey(node.ID), node.DeclaringType, node.Change, counterpart, node.CounterpartAbsent}))
	}
	for _, edge := range document.Graph.Edges {
		targets := []string{}
		for _, target := range edge.TargetIDs {
			targets = append(targets, nodeKey(target))
		}
		slices.Sort(targets)
		reasons := append([]string{}, edge.Reasons...)
		result["edge"] = append(result["edge"], canonicalJSON([]any{edge.Kind, nodeKey(edge.SourceID), targets, edge.Revision, edge.Line, edge.InvokedName, edge.ReceiverType, edge.Resolution, edge.Change, edge.Confidence, reasons}))
	}
	for _, block := range document.Blocks {
		symbols, context, changes := []string{}, []string{}, []string{}
		for _, id := range block.SymbolIDs {
			symbols = append(symbols, nodeKey(id))
		}
		for _, source := range block.StructuralContext {
			context = append(context, sliceKey(source))
		}
		for _, line := range block.ChangedLines {
			changes = append(changes, canonicalJSON(line))
		}
		slices.Sort(symbols)
		slices.Sort(context)
		slices.Sort(changes)
		result["block"] = append(result["block"], canonicalJSON([]any{block.FilePath, block.Kind, sourceKey(block.Base), sourceKey(block.Head), block.BaseExists, block.HeadExists, symbols, changes, context}))
	}
	for _, source := range document.DependencyContext {
		result["dependency"] = append(result["dependency"], sliceKey(source))
	}
	for _, source := range document.DeclarationContext {
		result["declaration"] = append(result["declaration"], sliceKey(source))
	}
	coverage := document.Coverage
	dispositions := []string{}
	for _, item := range coverage.ContextLines {
		dispositions = append(dispositions, "context:"+canonicalJSON(item))
	}
	for _, item := range coverage.ExcludedLines {
		dispositions = append(dispositions, "excluded:"+canonicalJSON(item))
	}
	slices.Sort(dispositions)
	result["coverage"] = []string{canonicalJSON([]any{coverage.ExpectedChangedLines, coverage.CoveredChangedLines, coverage.RequiredSourceLines, coverage.IsComplete, append([]string{}, coverage.Exceptions...), dispositions})}
	return result
}

func readParityJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, value); err != nil {
		t.Fatal(err)
	}
}

func writeParityJSON(t *testing.T, path string, value any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(value); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestParityComparisonIgnoresTechnicalIDsAndFindsSourceChanges(t *testing.T) {
	before := sampleBatch()
	before.Input.Graph = Graph{Nodes: []GraphNode{{ID: "n1", SourceSymbolID: "s1", Path: "A.java", Name: "f", Kind: 4, Revision: Head, StartLine: 2, EndLine: 4}}, Edges: []GraphEdge{{ID: "e1", Kind: "call", SourceID: "n1", TargetIDs: []string{"n1"}, Revision: Head}}}
	before.Input.Batch.Blocks[0].SymbolIDs = []string{"s1"}
	legacy := Document{Graph: before.Input.Graph, Blocks: before.Input.Batch.Blocks}
	var current Document
	if err := json.Unmarshal([]byte(canonicalJSON(legacy)), &current); err != nil {
		t.Fatal(err)
	}
	current.Graph.Nodes[0].ID, current.Graph.Nodes[0].SourceSymbolID = "other-node", "other-symbol"
	current.Graph.Edges[0].ID, current.Graph.Edges[0].SourceID, current.Graph.Edges[0].TargetIDs = "other-edge", "other-node", []string{"other-node"}
	current.Blocks[0].ID, current.Blocks[0].SymbolIDs = "other-block", []string{"other-symbol"}
	if diff := compareParityDocuments(legacy, current); len(diff) != 0 {
		t.Fatalf("technical IDs changed semantic parity: %v", diff)
	}
	current.Blocks[0].Head.Content = strings.Replace(current.Blocks[0].Head.Content, "return 2", "return 3", 1)
	if diff := compareParityDocuments(legacy, current); len(diff) != 2 || diff[0].Category != "block" || diff[1].Category != "block" || diff[0].Sha256 == diff[1].Sha256 {
		t.Fatal(fmt.Sprint("source changes did not produce two distinct source fingerprints: ", diff))
	}
}

func TestOfflineLegacyAnalysisIdentity(t *testing.T) {
	snapshotPath, planPath := os.Getenv("MARINADOREVIEW_LEGACY_SNAPSHOT"), os.Getenv("MARINADOREVIEW_LEGACY_PLAN")
	if snapshotPath == "" && planPath == "" {
		t.Skip("Set both legacy paths to compare an existing saved native document.")
	}
	var wrapped struct {
		Snapshot Snapshot `json:"snapshot"`
	}
	readParityJSON(t, snapshotPath, &wrapped)
	var plan struct {
		Document Document `json:"document"`
	}
	readParityJSON(t, planPath, &plan)
	current, err := analyzeWithSelection(wrapped.Snapshot, 1)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalJSON(current) != canonicalJSON(plan.Document) {
		t.Fatalf("legacy source analysis changed its saved document: graph %s vs %s", current.Graph.Sha256, plan.Document.Graph.Sha256)
	}
}

func TestParityEvidenceRequiresExactMembershipAndSourceHashes(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "bounded-proof.json")
	if err := os.WriteFile(source, []byte("source proof"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, legacy := filepath.Join(directory, "snapshot.json"), filepath.Join(directory, "legacy.json")
	if err := os.WriteFile(snapshot, []byte("snapshot-one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy-one"), 0600); err != nil {
		t.Fatal(err)
	}
	proof := parityEvidence{Version: 2}
	fixture := parityEvidenceCase{
		Rule: "nested-call-discovery", Regression: parityRegressionRules["nested-call-discovery"], Reason: "The outer source invocation is present.",
		ActualSnapshotSha256: Hash("snapshot-one"), LegacyDocumentSha256: Hash("legacy-one"),
		Paths:  map[string]string{"source": source, "actualSnapshot": snapshot, "legacyDocument": legacy},
		Sha256: map[string]string{"source": Hash("source proof"), "actualSnapshot": Hash("snapshot-one"), "legacyDocument": Hash("legacy-one")}, Differences: []string{"exact-difference"},
	}
	proof.Cases = append(proof.Cases, fixture)
	path := filepath.Join(directory, "evidence.json")
	writeParityJSON(t, path, proof)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	member, err := parityEvidenceMembership(path, Hash(string(raw)), Hash("snapshot-one"), Hash("legacy-one"))
	if err != nil || !member["nested-call-discovery:exact-difference"] || member["nested-call-discovery:other-difference"] {
		t.Fatalf("parity evidence accepted a blanket rule: %v %v", member, err)
	}
	if _, err = parityEvidenceMembership(path, Hash(string(raw)), Hash("snapshot-two"), Hash("legacy-one")); err == nil {
		t.Fatal("a repeated difference fingerprint reused evidence from another snapshot")
	}
	if _, err = parityEvidenceMembership(path, Hash(string(raw)), Hash("snapshot-one"), Hash("legacy-two")); err == nil {
		t.Fatal("a repeated difference fingerprint reused another legacy document")
	}
	if err = os.WriteFile(source, []byte("changed proof"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = parityEvidenceMembership(path, Hash(string(raw)), Hash("snapshot-one"), Hash("legacy-one")); err == nil {
		t.Fatal("changed bounded proof was accepted")
	}
}
