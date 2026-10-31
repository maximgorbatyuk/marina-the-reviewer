package usage

import "github.com/maximgorbatyuk/marina-the-reviewer/internal/stats"

// Counts preserves the shared statistics token shape for usage callers.
type Counts = stats.Counts

// Event preserves the shared statistics event shape for usage callers.
type Event = stats.Event

// Store preserves the shared statistics storage API for usage callers.
type Store = stats.Store

// Review preserves the shared review outcome shape for usage callers.
type Review = stats.Review

// DefaultPath returns the private statistics database path used by usage recording.
func DefaultPath() (string, error) { return stats.DefaultPath() }

// DefaultStore resolves private statistics storage and optional legacy import settings.
func DefaultStore() (Store, error) { return stats.DefaultStore() }
