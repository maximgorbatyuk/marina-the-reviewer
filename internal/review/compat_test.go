package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type compatibilityFixture struct {
	RunID           string          `json:"runId"`
	PassID          string          `json:"passId"`
	ClusterID       string          `json:"clusterId"`
	SystemPrompt    string          `json:"systemPrompt"`
	ResponseSchema  json.RawMessage `json:"responseSchema"`
	ValidationCases []struct {
		Name     string          `json:"name"`
		Prepared PreparedBatch   `json:"prepared"`
		Output   json.RawMessage `json:"output"`
		Policy   int             `json:"policy"`
		Result   json.RawMessage `json:"result"`
		Error    *string         `json:"error"`
	} `json:"validationCases"`
	WireCases []struct {
		Name  string     `json:"name"`
		Input BatchInput `json:"input"`
		Wire  string     `json:"wire"`
	} `json:"wireCases"`
	OutputBudgets []struct {
		Context  int    `json:"context"`
		Blocks   int    `json:"blocks"`
		Provider string `json:"provider"`
		Value    int    `json:"value"`
	} `json:"outputBudgets"`
	AnalysisCases []struct {
		Name        string   `json:"name"`
		Document    Document `json:"document"`
		Preparation struct {
			Files []struct {
				OldPath string          `json:"oldPath"`
				NewPath string          `json:"newPath"`
				Base    *SourceSnapshot `json:"base"`
				Head    *SourceSnapshot `json:"head"`
				Diff    struct {
					Hunks []struct {
						OldStart int      `json:"oldStart"`
						NewStart int      `json:"newStart"`
						Lines    []string `json:"lines"`
					} `json:"hunks"`
				} `json:"diff"`
			} `json:"files"`
		} `json:"preparation"`
		Planned struct {
			Batches []Batch `json:"batches"`
		}
	} `json:"analysisCases"`
}

func TestRecordedCSharpSourceSelection(t *testing.T) {
	f := loadCompatibility(t)
	for _, c := range f.AnalysisCases {
		t.Run(c.Name, func(t *testing.T) {
			s := Snapshot{IsComplete: true, Files: []FileSnapshot{}}
			for _, source := range c.Preparation.Files {
				file := FileSnapshot{OldPath: source.OldPath, NewPath: source.NewPath, Operation: "M", Base: source.Base, Head: source.Head}
				for _, h := range source.Diff.Hunks {
					oldLine, newLine := h.OldStart, h.NewStart
					for _, line := range h.Lines {
						switch line[0] {
						case '-':
							file.ChangedLines = append(file.ChangedLines, ChangedLine{Base, oldLine})
							oldLine++
						case '+':
							file.ChangedLines = append(file.ChangedLines, ChangedLine{Head, newLine})
							newLine++
						case ' ':
							oldLine++
							newLine++
						}
					}
				}
				s.Files = append(s.Files, file)
			}
			d, e := Analyze(s)
			if e != nil {
				t.Fatal(e)
			}
			sameJSON(t, marshal(d.Coverage), marshal(c.Document.Coverage))
			selection := func(blocks []Block) any {
				type selected struct {
					FilePath          string
					Kind              string
					Base, Head        *CodeSlice
					ChangedLines      []ChangedLine
					StructuralContext []CodeSlice
				}
				out := []selected{}
				for _, b := range blocks {
					out = append(out, selected{b.FilePath, b.Kind, b.Base, b.Head, b.ChangedLines, b.StructuralContext})
				}
				return out
			}
			sameJSON(t, marshal(selection(d.Blocks)), marshal(selection(c.Document.Blocks)))
		})
	}
}

func TestRecordedCSharpDependencyPlanning(t *testing.T) {
	f := loadCompatibility(t)
	for _, c := range f.AnalysisCases {
		t.Run(c.Name, func(t *testing.T) {
			p, e := Plan(c.Document, PlanOptions{ContextLength: 131072})
			if e != nil {
				t.Fatal(e)
			}
			if len(p.Batches) != len(c.Planned.Batches) {
				t.Fatal("batch count differs")
			}
			for i, b := range p.Batches {
				sameJSON(t, marshal(b.Input.Batch), marshal(c.Planned.Batches[i]))
			}
		})
	}
}

func loadCompatibility(t *testing.T) compatibilityFixture {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("..", "..", "testdata", "compat", "stages.json"))
	if e != nil {
		t.Fatal(e)
	}
	var f compatibilityFixture
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	return f
}
func sameJSON(t *testing.T, actual, expected []byte) {
	t.Helper()
	var a, b any
	if e := json.Unmarshal(actual, &a); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal(expected, &b); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("JSON mismatch: %s", jsonDifference(a, b, "$"))
	}
}
func jsonDifference(a, b any, path string) string {
	if reflect.DeepEqual(a, b) {
		return ""
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			break
		}
		for k, v := range bv {
			if d := jsonDifference(av[k], v, path+"."+k); d != "" {
				return d
			}
		}
		for k := range av {
			if _, ok := bv[k]; !ok {
				return path + "." + k + ": unexpected property"
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			break
		}
		for i, v := range av {
			if d := jsonDifference(v, bv[i], fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
	}
	return fmt.Sprintf("%s: got %#v, want %#v", path, a, b)
}
func TestRecordedCSharpValidationParity(t *testing.T) {
	f := loadCompatibility(t)
	for _, c := range f.ValidationCases {
		t.Run(c.Name, func(t *testing.T) {
			r, e := Validate(c.Prepared, c.Output, Identity{f.RunID, f.PassID, f.ClusterID, c.Policy})
			if c.Error != nil {
				if e == nil {
					t.Fatalf("expected error %s", *c.Error)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			sameJSON(t, marshal(r), c.Result)
		})
	}
}
func TestRecordedCSharpWireParity(t *testing.T) {
	f := loadCompatibility(t)
	for _, c := range f.WireCases {
		t.Run(c.Name, func(t *testing.T) { sameJSON(t, []byte(SerializeInput(c.Input)), []byte(c.Wire)) })
	}
}
func TestRecordedCSharpPolicyParity(t *testing.T) {
	f := loadCompatibility(t)
	if DefaultSystemPrompt() != f.SystemPrompt {
		t.Fatal("embedded default prompt differs from C# effective prompt")
	}
	sameJSON(t, ResponseSchema(), f.ResponseSchema)
	for _, c := range f.OutputBudgets {
		if got := OutputBudget(c.Context, c.Blocks, c.Provider); got != c.Value {
			t.Errorf("output budget %s: got %d, want %d", c.Provider, got, c.Value)
		}
	}
}
