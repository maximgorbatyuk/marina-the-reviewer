package review

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type reviewCountingContext struct {
	context.Context
	cancel       context.CancelFunc
	after, calls int
}

func newReviewCountingContext(after int) *reviewCountingContext {
	ctx, cancel := context.WithCancel(context.Background())
	return &reviewCountingContext{Context: ctx, cancel: cancel, after: after}
}

func (ctx *reviewCountingContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.after {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func reviewContextSnapshot() Snapshot {
	before := "class ContextSource {\n"
	for i := 0; i < 24; i++ {
		before += fmt.Sprintf(" int method%d() { return 0; }\n", i)
	}
	before += "}\n"
	after := strings.Replace(before, "return 0", "return 1", 1)
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	diff := "@@ -2 +2 @@\n- int method0() { return 0; }\n+ int method0() { return 1; }\n"
	hunks, changed := parseDiff(diff)
	snapshot := Snapshot{Version: 1, BaseSha: base, StartSha: base, HeadSha: head, IsComplete: true,
		Files: []FileSnapshot{{OldPath: "ContextSource.java", NewPath: "ContextSource.java", Operation: "M", Diff: diff, Hunks: hunks, ChangedLines: changed,
			Base: &SourceSnapshot{Path: "ContextSource.java", Revision: Base, CommitSha: base, Sha256: Hash(before), Content: before},
			Head: &SourceSnapshot{Path: "ContextSource.java", Revision: Head, CommitSha: head, Sha256: Hash(after), Content: after}}}}
	snapshot.ManifestSha256 = snapshotManifest(snapshot)
	return snapshot
}

func TestAnalysisContextCancellationInsideOneFile(t *testing.T) {
	ctx := newReviewCountingContext(12)
	defer ctx.cancel()
	_, err := AnalyzeContext(ctx, reviewContextSnapshot())
	if !errors.Is(err, context.Canceled) || ctx.calls < ctx.after {
		t.Fatalf("analysis did not stop inside its work: polls=%d, error=%v", ctx.calls, err)
	}
}

func TestAnalysisContextBackgroundKeepsDocumentIdentity(t *testing.T) {
	snapshot := reviewContextSnapshot()
	want, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := AnalyzeContext(context.Background(), snapshot)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("context changed the document: %v", err)
	}
}

func TestSnapshotAndDocumentValidationObserveMidOperationCancellation(t *testing.T) {
	snapshot := reviewContextSnapshot()
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	ctx := newReviewCountingContext(5)
	defer ctx.cancel()
	if err := ValidateSnapshotContext(ctx, snapshot); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot validation ignored cancellation: %v", err)
	}
	document, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ctx = newReviewCountingContext(8)
	defer ctx.cancel()
	if err := ValidateDocumentContext(ctx, document); !errors.Is(err, context.Canceled) {
		t.Fatalf("document validation ignored cancellation: %v", err)
	}
}

func TestSourceTokenOperationsObserveCancellationWithinOneLargeInput(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"mask", func(ctx context.Context) error {
			_, err := sanitizeJavaContext(ctx, strings.Repeat("x", 1<<20))
			return err
		}},
		{"tokens", func(ctx context.Context) error {
			_, err := javaTokensContext(ctx, strings.Repeat("x", 1<<20))
			return err
		}},
		{"declarations", func(ctx context.Context) error {
			tokens := make([]javaToken, 1000)
			for i := range tokens {
				tokens[i].text = ";"
			}
			_, err := declarationTokensContext(ctx, tokens)
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			ctx := newReviewCountingContext(3)
			defer ctx.cancel()
			if err := operation.run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("source operation ignored cancellation: %v", err)
			}
		})
	}
}

func TestContextNormalizationCancelsWithoutChangingInputOrLaterProbes(t *testing.T) {
	for _, operation := range []string{"build", "normalize", "trial", "dependencies"} {
		t.Run(operation, func(t *testing.T) {
			document := contextTrialFixture()
			batch := Batch{Blocks: document.Blocks}
			index := newContextIndex(document)
			before := defaultJSON(document)
			ctx := newReviewCountingContext(12)
			defer ctx.cancel()
			var err error
			switch operation {
			case "build":
				_, err = newContextIndexContext(ctx, document)
			case "normalize":
				_, err = index.normalizeContext(ctx, batch)
			case "trial":
				_, err = index.normalizeTrialContext(ctx, batch)
			case "dependencies":
				_, err = index.decorateDependenciesContext(ctx, batch)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("normalization ignored cancellation: %v", err)
			}
			if defaultJSON(document) != before {
				t.Fatal("cancelled normalization changed the input")
			}
			if !reflect.DeepEqual(index.normalizeTrial(batch), legacyContextNormalize(batch, document)) {
				t.Fatal("cancelled operation changed a later independent probe")
			}
		})
	}
}

func TestContextInnerCollectionsObserveCancellation(t *testing.T) {
	contexts := make([]CodeSlice, 1000)
	for i := range contexts {
		contexts[i] = CodeSlice{Path: "File.java", Revision: Head, StartLine: i + 2, EndLine: i + 2, Content: "int value;"}
	}
	for _, operation := range []string{"normalize", "construct", "seal", "targets"} {
		t.Run(operation, func(t *testing.T) {
			ctx := newReviewCountingContext(100)
			defer ctx.cancel()
			document := Document{Blocks: []Block{{StructuralContext: contexts}}}
			var err error
			switch operation {
			case "normalize":
				_, err = newContextIndex(document).normalizeContext(ctx, Batch{Blocks: document.Blocks})
			case "construct":
				_, err = newContextIndexContext(ctx, document)
			case "seal":
				index := contextSpanIndex{{"File.java", Head}: make([]contextInterval, 1000)}
				err = index.sealContext(ctx)
			case "targets":
				targets := make([]string, 1000)
				document = Document{Graph: Graph{Edges: []GraphEdge{{Kind: "call", TargetIDs: targets}}}}
				_, err = newContextIndex(document).decorateDependenciesContext(ctx, Batch{})
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s did not cancel inside its collection: polls=%d, error=%v", operation, ctx.calls, err)
			}
		})
	}
}
