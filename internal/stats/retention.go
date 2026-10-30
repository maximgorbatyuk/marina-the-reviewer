package stats

import (
	"errors"
	"time"
)

func maintain(s *session, now time.Time) error {
	value, err := meta(s.ctx, s.db, "last_maintenance")
	if err != nil {
		return errors.New("cannot read statistics maintenance state")
	}
	if value != "" {
		at, err := parseStamp(value)
		if err != nil {
			return errors.New("invalid statistics maintenance state")
		}
		if now.Sub(at) < 24*time.Hour {
			return nil
		}
	}
	tx, err := s.db.BeginTx(s.ctx, nil)
	if err != nil {
		return errors.New("cannot start statistics maintenance")
	}
	defer tx.Rollback()
	cutoff := stamp(now.Add(-RetentionDays * 24 * time.Hour))
	for _, table := range []string{"api_requests", "review_runs"} {
		if _, err := tx.ExecContext(s.ctx, "DELETE FROM "+table+" WHERE started_at<?", cutoff); err != nil {
			return errors.New("cannot remove expired statistics")
		}
	}
	if err := putMeta(s.ctx, tx, "last_maintenance", stamp(now)); err != nil {
		return errors.New("cannot save statistics maintenance state")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit statistics maintenance")
	}
	// Space reclamation can wait when another SQLite client holds a read snapshot.
	_, _ = s.db.ExecContext(s.ctx, "PRAGMA incremental_vacuum(256); PRAGMA wal_checkpoint(PASSIVE)")
	return nil
}
