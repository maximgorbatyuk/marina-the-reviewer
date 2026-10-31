package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

func preparedComparisonFixture() review.PreparedPlan {
	text, line, confidence := "value", 1, 0.5
	graph := review.Graph{Sha256: "graph", Limitations: []string{"limitation"},
		Nodes: []review.GraphNode{{ID: "node", DeclaringType: &text, CounterpartID: &text}},
		Edges: []review.GraphEdge{{ID: "edge", TargetIDs: []string{"target"}, Reasons: []string{"reason"}, Line: &line, InvokedName: &text, ReceiverType: &text, Confidence: &confidence}}}
	block := document().Blocks[0]
	base := *block.Head
	block.Base = &base
	block.SymbolIDs = []string{"symbol"}
	block.Limitations = []string{"limitation"}
	block.StructuralContext = []review.CodeSlice{base}
	return review.PreparedPlan{Version: 3, Purpose: 3, GraphSha256: "graph", Limitations: []string{"limitation"}, GraphPreflight: json.RawMessage(`{"ok":true}`), SplitReason: &text,
		Batches: []review.PreparedBatch{{Input: review.BatchInput{Graph: graph, Batch: review.Batch{Blocks: []review.Block{block}, FileParts: []review.FilePart{{Path: "a.go"}}}, Limitations: []string{"limitation"}, Task: json.RawMessage(`{"task":true}`)}, TokenCount: &review.TokenCount{}, ParentTaskID: &text}}}
}

func TestPreparedPlanComparisonSerializesSharedGraphOnce(t *testing.T) {
	plan := preparedComparisonFixture()
	plan.Batches[0].Input.Graph.Nodes = make([]review.GraphNode, 1024)
	for index := range plan.Batches[0].Input.Graph.Nodes {
		plan.Batches[0].Input.Graph.Nodes[index] = review.GraphNode{ID: fmt.Sprint(index), Signature: strings.Repeat("x", 4096)}
	}
	first := plan.Batches[0]
	for range 15 {
		plan.Batches = append(plan.Batches, first)
	}
	nodes := 0
	compare := func(ctx context.Context, a, b any) (bool, error) {
		if _, ok := a.(review.GraphNode); ok {
			nodes++
		}
		if batch, ok := a.(review.PreparedBatch); ok && len(batch.Input.Graph.Nodes) != 0 {
			t.Fatal("batch comparison contains the shared graph")
		}
		if header, ok := a.(review.PreparedPlan); ok && len(header.Batches) != 0 {
			t.Fatal("comparison contains all batches")
		}
		return canonicalValuesEqualContext(ctx, a, b)
	}
	equal, err := preparedPlansEqualContext(context.Background(), plan, plan, compare)
	if err != nil || !equal || nodes != 1024 {
		t.Fatalf("shared graph repeated: equal=%v err=%v nodes=%d", equal, err, nodes)
	}
}

func TestPreparedPlanComparisonReturnsCancellationInsideSharedGraph(t *testing.T) {
	plan := preparedComparisonFixture()
	plan.Batches[0].Input.Graph.Nodes = make([]review.GraphNode, 100)
	ctx := cancellationOnPoll(12, nil)
	defer ctx.cancel()
	if _, err := preparedPlansEqualContext(ctx, plan, plan, canonicalValuesEqualContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("comparison cancellation changed: %v", err)
	}
}

func TestPreparedPlanComparisonDoesNotTrustGraphHashOrSharedHeader(t *testing.T) {
	for _, change := range []string{"node", "header"} {
		t.Run(change, func(t *testing.T) {
			left := preparedComparisonFixture()
			left.Batches = append(left.Batches, left.Batches[0])
			right := left
			right.Batches = append([]review.PreparedBatch{}, left.Batches...)
			if change == "node" {
				right.Batches[1].Input.Graph.Nodes = append([]review.GraphNode{}, left.Batches[1].Input.Graph.Nodes...)
				right.Batches[1].Input.Graph.Nodes[0].Signature = "tampered"
			} else {
				right.Batches[1].Input.Graph.Sha256 = "tampered"
			}
			if equal, err := preparedPlansEqualContext(context.Background(), left, right, canonicalValuesEqualContext); err == nil && equal {
				t.Fatal("graph identity cache hid a mismatch")
			}
		})
	}
}

func TestPreparedPlanComparisonPreservesCanonicalJsonSemantics(t *testing.T) {
	for _, variant := range []string{"invalid-utf8", "null-empty", "raw-whitespace", "malformed"} {
		t.Run(variant, func(t *testing.T) {
			left, right := preparedComparisonFixture(), preparedComparisonFixture()
			switch variant {
			case "invalid-utf8":
				left.Batches[0].Input.Graph.Nodes[0].ID = string([]byte{0xff})
				right.Batches[0].Input.Graph.Nodes[0].ID = string([]byte{0xfe})
			case "null-empty":
				left.Batches[0].Input.Graph.Nodes = nil
				right.Batches[0].Input.Graph.Nodes = []review.GraphNode{}
			case "raw-whitespace":
				right.GraphPreflight = json.RawMessage(` { "ok" : true } `)
			case "malformed":
				right.GraphPreflight = json.RawMessage(`!`)
			}
			a, aErr := json.Marshal(left)
			b, bErr := json.Marshal(right)
			want := aErr == nil && bErr == nil && string(a) == string(b)
			equal, err := preparedPlansEqualContext(context.Background(), left, right, canonicalValuesEqualContext)
			if (err == nil && equal) != want {
				t.Fatalf("canonical semantics changed: want=%v equal=%v err=%v", want, equal, err)
			}
		})
	}
}

func TestPreparedPlanComparisonRejectsEveryFieldMutation(t *testing.T) {
	fixture := preparedComparisonFixture()
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var paths [][]int
	var collect func(reflect.Value, []int)
	collect = func(value reflect.Value, path []int) {
		switch value.Kind() {
		case reflect.Struct:
			for i := 0; i < value.NumField(); i++ {
				collect(value.Field(i), append(append([]int{}, path...), i))
			}
		case reflect.Pointer:
			paths = append(paths, append([]int{}, path...))
			if !value.IsNil() {
				collect(value.Elem(), append(append([]int{}, path...), -1))
			}
		case reflect.Slice:
			paths = append(paths, append([]int{}, path...))
			if value.Len() > 0 && value.Type() != reflect.TypeFor[json.RawMessage]() {
				collect(value.Index(0), append(append([]int{}, path...), -2))
			}
		default:
			paths = append(paths, append([]int{}, path...))
		}
	}
	collect(reflect.ValueOf(fixture), nil)
	for index, path := range paths {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			var changed review.PreparedPlan
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			value := reflect.ValueOf(&changed).Elem()
			for _, step := range path {
				switch step {
				case -1:
					value = value.Elem()
				case -2:
					value = value.Index(0)
				default:
					value = value.Field(step)
				}
			}
			switch value.Kind() {
			case reflect.String:
				value.SetString(value.String() + "changed")
			case reflect.Bool:
				value.SetBool(!value.Bool())
			case reflect.Int:
				value.SetInt(value.Int() + 1)
			case reflect.Float64:
				value.SetFloat(value.Float() + 0.25)
			case reflect.Pointer:
				if value.IsNil() {
					value.Set(reflect.New(value.Type().Elem()))
				} else {
					value.SetZero()
				}
			case reflect.Slice:
				if value.IsNil() {
					value.Set(reflect.MakeSlice(value.Type(), 0, 0))
				} else {
					value.SetZero()
				}
			default:
				t.Fatalf("unsupported field kind: %s", value.Kind())
			}
			if equal, err := preparedPlansEqualContext(context.Background(), fixture, changed, canonicalValuesEqualContext); err == nil && equal {
				t.Fatalf("field mutation was accepted: %v", path)
			}
		})
	}
}
