package engine

import (
 "context"
 "errors"
 "github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
 "github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

func BuildContext(ctx context.Context, snapshot Snapshot, route config.Route, prompt string) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if snapshot.SchemaVersion != 1 || snapshot.Kind != "ReviewSnapshot" || snapshot.Compatibility != Contract() {
		return Plan{}, errors.New("unsupported snapshot document")
	}
	if err := review.ValidateSnapshotContext(ctx, snapshot.Snapshot); err != nil {
		return Plan{}, err
	}
	doc, err := review.AnalyzeContext(ctx, snapshot.Snapshot)
	if err != nil {
		return Plan{}, err
	}
	return BuildDocumentContext(ctx, doc, route, prompt, snapshot.Task)
}
