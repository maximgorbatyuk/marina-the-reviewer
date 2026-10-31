package review

import (
	"context"
	"slices"
)

type cpuContextPoll struct {
	context context.Context
	steps   uint32
}

func (poll *cpuContextPoll) check() error {
	poll.steps++
	if poll.steps&255 == 0 {
		return poll.context.Err()
	}
	return nil
}

type contextSortInterruption struct{ err error }

// A canceled sort must stop before it can return a partial order.
func sortWithContext[T any](ctx context.Context, values []T, compare func(T, T) int, stable bool) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if value := recover(); value != nil {
			if interruption, ok := value.(contextSortInterruption); ok {
				err = interruption.err
			} else {
				panic(value)
			}
		}
	}()
	poll := cpuContextPoll{context: ctx}
	checked := func(a, b T) int {
		if interruption := poll.check(); interruption != nil {
			panic(contextSortInterruption{interruption})
		}
		return compare(a, b)
	}
	if stable {
		slices.SortStableFunc(values, checked)
	} else {
		slices.SortFunc(values, checked)
	}
	return ctx.Err()
}
func sortStableContext[T any](ctx context.Context, values []T, compare func(T, T) int) error {
	return sortWithContext(ctx, values, compare, true)
}
func sortOrdinalByContext[T any](ctx context.Context, values []T, text func(T) string) error {
	return sortWithContext(ctx, values, func(a, b T) int { return ordinalCompare(text(a), text(b)) }, false)
}
func sortOrdinalContext(ctx context.Context, values []string) error {
	return sortOrdinalByContext(ctx, values, func(value string) string { return value })
}
