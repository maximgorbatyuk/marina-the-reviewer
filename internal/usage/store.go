package usage

import "github.com/maximgorbatyuk/marina-the-reviewer/internal/stats"

type Counts = stats.Counts
type Event = stats.Event
type Store = stats.Store
type Review = stats.Review

func DefaultPath() (string, error) { return stats.DefaultPath() }
func DefaultStore() (Store, error) { return stats.DefaultStore() }
