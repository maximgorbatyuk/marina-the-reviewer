package review

import "context"

func AnalyzeContext(ctx context.Context, snapshot Snapshot) (Document, error) {
	return analyzeWithSelectionContext(ctx, snapshot, CurrentSelectionVersion)
}
