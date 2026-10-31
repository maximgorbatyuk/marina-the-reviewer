package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

type cancelOnPollContext struct {
	context.Context
	cancel   context.CancelFunc
	limit    int32
	polls    atomic.Int32
	observed func()
}

func cancellationOnPoll(limit int, observed func()) *cancelOnPollContext {
	ctx, cancel := context.WithCancel(context.Background())
	return &cancelOnPollContext{Context: ctx, cancel: cancel, limit: int32(limit), observed: observed}
}

func (c *cancelOnPollContext) Err() error {
	if c.polls.Add(1) == c.limit {
		c.cancel()
		if c.observed != nil {
			c.observed()
		}
	}
	return c.Context.Err()
}

func TestEngineContextEntryCancellationIsNotAValidationFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, route := routeFor(t, "http://127.0.0.1:1", "")
	if _, err := BuildContext(ctx, Snapshot{}, route, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot cancellation changed: %v", err)
	}
	if _, err := BuildDocumentContext(ctx, document(), route, "", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("document cancellation changed: %v", err)
	}
	if err := (Plan{}).ValidateContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("plan cancellation changed: %v", err)
	}
}

func TestEngineDeadlineErrorIsNotABudgetFailure(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, route := routeFor(t, "http://127.0.0.1:1", "")
	if _, err := BuildDocumentContext(ctx, document(), route, "", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("document deadline changed: %v", err)
	}
	if err := (Plan{}).ValidateContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("plan deadline changed: %v", err)
	}
	bundle, err := OpenBundle(filepath.Join(t.TempDir(), "bundle"), "deadline")
	if err != nil {
		t.Fatal(err)
	}
	before := privateFileTree(t, bundle.Directory)
	if err = bundle.SavePlanContext(ctx, Plan{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("save deadline changed: %v", err)
	}
	if err = bundle.SaveSnapshotContext(ctx, Snapshot{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("snapshot deadline changed: %v", err)
	}
	if _, err = bundle.LoadPlanContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("load deadline changed: %v", err)
	}
	if !reflect.DeepEqual(before, privateFileTree(t, bundle.Directory)) {
		t.Fatal("expired operation wrote artifacts")
	}
}

func TestEngineReplanCancellationDoesNotSupersedeSource(t *testing.T) {
	_, route := routeFor(t, "http://127.0.0.1:1", "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := state{Works: []work{{ID: "parent", Role: "primary", Prepared: plan.Prepared.Batches[0]}}}
	ctx := cancellationOnPoll(2, nil)
	defer ctx.cancel()
	replanned, err := replanWorkContext(ctx, &s, 0, route.Primary)
	if replanned || !errors.Is(err, context.Canceled) {
		t.Fatalf("split cancellation changed: replanned=%v err=%v", replanned, err)
	}
	if len(s.Works) != 1 || s.Works[0].Superseded || s.Works[0].Failure != "" {
		t.Fatal("cancelled split changed source ownership")
	}
}

func TestEngineFallbackScopeCancellationInsideCoverage(t *testing.T) {
	_, route := routeFor(t, "http://127.0.0.1:1", "")
	doc := document()
	doc.Coverage.ContextLines = []review.ChangeDisposition{{Path: "a.go", Revision: review.Head, Line: 1}}
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := cancellationOnPoll(3, nil)
	defer ctx.cancel()
	if _, err = scopeDocumentContext(ctx, doc, plan.Prepared.Batches[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("scope cancellation changed: %v", err)
	}
	if len(doc.Coverage.ContextLines) != 1 || len(doc.Blocks) != 1 {
		t.Fatal("cancelled fallback changed the source")
	}
}

func TestEngineSavedSourceCancellationInsideOneSlice(t *testing.T) {
	doc := document()
	doc.Blocks[0].Head.Content = strings.Repeat("source\n", 500)
	doc.Blocks[0].Head.EndLine = 500
	ctx := cancellationOnPoll(10, nil)
	defer ctx.cancel()
	if _, err := sourceLinesContext(ctx, review.PreparedBatch{Input: review.BatchInput{Batch: review.Batch{Blocks: doc.Blocks}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("source cancellation changed: %v", err)
	}
}

func TestEngineRunCancellationDuringValidationDoesNotCreateExecution(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		okModel(w)
	}))
	defer server.Close()
	cfg, route := routeFor(t, server.URL, "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "execution")
	ctx := cancellationOnPoll(2, nil)
	defer ctx.cancel()
	if _, err = Run(ctx, plan, cfg, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("validation cancellation changed: %v", err)
	}
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) || calls.Load() != 0 {
		t.Fatalf("cancelled validation started execution: stat=%v calls=%d", err, calls.Load())
	}
}

func TestBundleSavePlanStopsWritesAfterObservedCancellation(t *testing.T) {
	_, route := routeFor(t, "http://127.0.0.1:1", "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	observations := 0
	probe, err := OpenBundle(filepath.Join(t.TempDir(), "count"), "count-save-polls")
	if err != nil {
		t.Fatal(err)
	}
	count := cancellationOnPoll(0, nil)
	defer count.cancel()
	if err = probe.SavePlanContext(count, plan); err != nil {
		t.Fatal(err)
	}
	total := int(count.polls.Load())
	seen := map[int]bool{}
	for _, limit := range []int{1, 2, 3, total / 4, total / 2, 3 * total / 4, total - 4, total - 3, total - 2, total - 1, total} {
		if limit < 1 || seen[limit] {
			continue
		}
		seen[limit] = true
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			bundle, err := OpenBundle(filepath.Join(t.TempDir(), "bundle"), "cancel-fence")
			if err != nil {
				t.Fatal(err)
			}
			var atCancel map[string]string
			ctx := cancellationOnPoll(limit, func() { atCancel = privateFileTree(t, bundle.Directory) })
			defer ctx.cancel()
			err = bundle.SavePlanContext(ctx, plan)
			if atCancel == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			observations++
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("save cancellation changed: %v", err)
			}
			if after := privateFileTree(t, bundle.Directory); !reflect.DeepEqual(atCancel, after) {
				t.Fatalf("write occurred after cancellation was observed: before=%v after=%v", atCancel, after)
			}
		})
	}
	if observations < 5 {
		t.Fatalf("save boundaries were not exercised: %d", observations)
	}
}

func TestBundleLoadPlanCancellationRetainsUncancelledValidation(t *testing.T) {
	_, route := routeFor(t, "http://127.0.0.1:1", "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := OpenBundle(filepath.Join(t.TempDir(), "bundle"), "cancel-load")
	if err != nil {
		t.Fatal(err)
	}
	if err = bundle.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	before := privateFileTree(t, bundle.Directory)
	ctx := cancellationOnPoll(3, nil)
	defer ctx.cancel()
	if _, err = bundle.LoadPlanContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("load cancellation changed: %v", err)
	}
	if !reflect.DeepEqual(before, privateFileTree(t, bundle.Directory)) {
		t.Fatal("load wrote files")
	}
	loaded, err := bundle.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	loaded.Prepared.Batches[0].Input.Batch.Blocks[0].ChangedLines = []review.ChangedLine{}
	if err = loaded.ValidateContext(context.Background()); err == nil {
		t.Fatal("cancellation enabled source-validation bypass")
	}
}

func privateFileTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		// Cancellation can remove temporary files without changing a committed artifact.
		if strings.HasPrefix(entry.Name(), ".marinadoreview-") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = fmt.Sprintf("%x", sha256.Sum256(raw))
		return nil
	})
	if err != nil {
		t.Error(err)
	}
	return result
}
