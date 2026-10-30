package stats

import (
	"database/sql"
	"errors"
	"time"
)

const reviewColumns = "run_id,pass_id,plan_hash,started_at,finished_at,status,checked_blocks,warning_findings,error_findings,critical_findings"

func validReview(r Review) error {
	if !validText(r.RunID, true) || !validText(r.PassID, true) || !validText(r.PlanHash, true) || !validTime(r.StartedAt) {
		return errors.New("invalid review statistics identity")
	}
	switch r.Status {
	case "unfinished":
		if r.FinishedAt != nil {
			return errors.New("unfinished review has a finish time")
		}
	case "complete", "partial", "failed", "canceled":
		if r.FinishedAt == nil || !validTime(*r.FinishedAt) || r.FinishedAt.Before(r.StartedAt) {
			return errors.New("invalid review finish time")
		}
	default:
		return errors.New("invalid review statistics status")
	}
	for _, n := range []int64{r.CheckedBlocks, r.WarningFindings, r.ErrorFindings, r.CriticalFindings} {
		if n < 0 {
			return errors.New("invalid review statistics counter")
		}
	}
	return nil
}
func scanReview(row rowScanner) (Review, error) {
	var r Review
	var started string
	var finished sql.NullString
	err := row.Scan(&r.RunID, &r.PassID, &r.PlanHash, &started, &finished, &r.Status, &r.CheckedBlocks, &r.WarningFindings, &r.ErrorFindings, &r.CriticalFindings)
	if err != nil {
		return r, err
	}
	r.StartedAt, err = parseStamp(started)
	if err != nil {
		return r, err
	}
	if finished.Valid {
		at, e := parseStamp(finished.String)
		if e != nil {
			return r, e
		}
		r.FinishedAt = &at
	}
	return r, validReview(r)
}
func (s Store) SaveReview(r Review) error {
	if err := validReview(r); err != nil {
		return err
	}
	now := s.now()
	if r.StartedAt.Before(now.Add(-RetentionDays * 24 * time.Hour)) {
		return nil
	}
	opened, err := s.open(true, now)
	if err != nil {
		return err
	}
	defer opened.close()
	tx, err := opened.db.BeginTx(opened.ctx, nil)
	if err != nil {
		return errors.New("cannot start review statistics write")
	}
	defer tx.Rollback()
	previous, err := scanReview(tx.QueryRowContext(opened.ctx, "SELECT "+reviewColumns+" FROM review_runs WHERE run_id=? AND pass_id=?", r.RunID, r.PassID))
	if err == nil {
		if previous.PlanHash != r.PlanHash || !previous.StartedAt.Equal(r.StartedAt) {
			return errors.New("statistics contain conflicting review identities")
		}
		if r.Status == "unfinished" && previous.Status != "unfinished" {
			return nil
		}
		sameCounters := previous.CheckedBlocks == r.CheckedBlocks && previous.WarningFindings == r.WarningFindings && previous.ErrorFindings == r.ErrorFindings && previous.CriticalFindings == r.CriticalFindings
		if previous.Status == r.Status && previous.Status != "unfinished" && sameCounters {
			return nil
		}
		if previous.Status == "complete" {
			if r.Status != "complete" {
				return nil
			}
			return errors.New("statistics contain conflicting completed review results")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return errors.New("cannot read review statistics")
	}
	var end any
	if r.FinishedAt != nil {
		end = stamp(*r.FinishedAt)
	}
	_, err = tx.ExecContext(opened.ctx, `INSERT INTO review_runs (`+reviewColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(run_id,pass_id) DO UPDATE SET finished_at=excluded.finished_at,status=excluded.status,checked_blocks=excluded.checked_blocks,warning_findings=excluded.warning_findings,error_findings=excluded.error_findings,critical_findings=excluded.critical_findings`, r.RunID, r.PassID, r.PlanHash, stamp(r.StartedAt), end, r.Status, r.CheckedBlocks, r.WarningFindings, r.ErrorFindings, r.CriticalFindings)
	if err != nil {
		return errors.New("cannot write review statistics")
	}
	if err := earliest(opened.ctx, tx, "review_tracking_since", r.StartedAt); err != nil {
		return errors.New("cannot write review statistics metadata")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit review statistics")
	}
	return nil
}
