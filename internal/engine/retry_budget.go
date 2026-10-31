package engine

import (
	"context"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
)

func retryFitsDeadline(ctx context.Context, timeoutSeconds int, delay time.Duration) bool {
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return true
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = config.DefaultRequestTimeoutSeconds
	}
	return time.Until(deadline) > delay+time.Duration(timeoutSeconds)*time.Second
}
