package review

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPlanningContextCancellationIsNotBudgetFailure(t *testing.T) {
	document, err := Analyze(reviewContextSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = PlanContext(ctx, document, PlanOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("planning lost cancellation: %v", err)
	}
	ctxCounter := newReviewCountingContext(24)
	defer ctxCounter.cancel()
	if _, err = PlanContext(ctxCounter, document, PlanOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("planning did not stop during work: %v polls%d", err, ctxCounter.calls)
	}
	parent := sampleBatch()
	parent.ReplanDepth = 3
	if _, err = SplitBatchContext(ctx, parent, PlanOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("replan lost cancellation: %v", err)
	}
	if _, err = RefreshPreparedContext(ctx, parent); !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh lost cancellation: %v", err)
	}
}
func TestPlanningContextBackgroundKeepsPreparedIdentity(t *testing.T) {
	document, err := Analyze(reviewContextSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	want, err := Plan(document, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := PlanContext(context.Background(), document, PlanOptions{})
	if err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatalf("context changed prepared output: %v", err)
	}
}
func TestSerializationAndTokenCountObserveCancellation(t *testing.T) {
	input := sampleBatch().Input
	for i := 0; i < 512; i++ {
		input.Batch.Blocks = append(input.Batch.Blocks, input.Batch.Blocks[0])
	}
	ctx := newReviewCountingContext(4)
	defer ctx.cancel()
	if _, err := SerializeInputContext(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("serialization ignored cancellation: %v polls%d", err, ctx.calls)
	}
	ctx = newReviewCountingContext(4)
	defer ctx.cancel()
	if _, err := CountTokensContext(ctx, "system", strings.Repeat("Ж😀", 65536), json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("token count ignored cancellation: %v polls%d", err, ctx.calls)
	}
}

func TestPlanningContextKeepsDeadlineErrors(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	input := sampleBatch()
	operations := []func() error{
		func() error { _, err := PlanContext(ctx, Document{}, PlanOptions{}); return err },
		func() error { _, err := SplitBatchContext(ctx, input, PlanOptions{}); return err },
		func() error { _, err := RefreshPreparedContext(ctx, input); return err },
		func() error { _, err := SerializeInputContext(ctx, input.Input); return err },
		func() error { _, err := ProjectGraphContext(ctx, input.Input.Graph, input.Input.Batch); return err },
		func() error { _, err := CountTokensContext(ctx, "", "", json.RawMessage(`{}`)); return err },
	}
	for i, operation := range operations {
		if err := operation(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("operation %d changed deadline: %v", i, err)
		}
	}
}
