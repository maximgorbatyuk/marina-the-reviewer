package stats

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode"
)

func validText(value string, required bool) bool {
	return (!required || value != "") && len(value) <= 2048 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}
func validTime(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() >= 1 && at.UTC().Year() <= 9999
}
func validEvent(e Event) error {
	if e.Version != 1 || (e.Kind != "start" && e.Kind != "finish") || !validTime(e.At) || !validTime(e.StartedAt) || (e.Kind == "finish" && !validText(e.Status, true)) {
		return errors.New("invalid usage event format")
	}
	for _, value := range []string{e.ID, e.ProviderID, e.Provider, e.Profile, e.Model, e.Operation} {
		if !validText(value, true) {
			return errors.New("invalid usage event metadata")
		}
	}
	if !validText(e.ProviderKind, false) {
		return errors.New("invalid usage provider kind")
	}
	if !validText(e.RunID, false) || !validText(e.PassID, false) || (e.RunID == "") != (e.PassID == "") {
		return errors.New("invalid usage review identity")
	}
	for _, n := range []*int64{e.Counts.Input, e.Counts.Output, e.Counts.Total} {
		if n != nil && *n < 0 {
			return errors.New("invalid usage token counter")
		}
	}
	if e.HTTPStatus != 0 && (e.HTTPStatus < 100 || e.HTTPStatus > 599) || e.Kind == "start" && (e.ResponsePayload != nil || e.ResponseComplete != nil || e.HTTPStatus != 0) {
		return errors.New("invalid usage response metadata")
	}
	return nil
}

const baseEventColumns = "request_id,kind,at,started_at,provider_id,provider,profile,model,operation,status,input_tokens,output_tokens,total_tokens,run_id,pass_id"
const eventColumns = baseEventColumns + ",provider_kind"

type rowScanner interface{ Scan(...any) error }

func scanEvent(row rowScanner) (Event, error) {
	var e Event
	var at, started string
	var input, output, total sql.NullInt64
	var providerKind sql.NullString
	err := row.Scan(&e.ID, &e.Kind, &at, &started, &e.ProviderID, &e.Provider, &e.Profile, &e.Model, &e.Operation, &e.Status, &input, &output, &total, &e.RunID, &e.PassID, &providerKind)
	if err != nil {
		return e, err
	}
	e.Version = 1
	e.ProviderKind = providerKind.String
	e.At, err = parseStamp(at)
	if err != nil {
		return e, err
	}
	e.StartedAt, err = parseStamp(started)
	if err != nil {
		return e, err
	}
	convert := func(n sql.NullInt64) *int64 {
		if !n.Valid {
			return nil
		}
		return &n.Int64
	}
	e.Counts = Counts{convert(input), convert(output), convert(total)}
	return e, validEvent(e)
}
func sameRequest(a, b Event) bool {
	return a.StartedAt.Equal(b.StartedAt) && a.ProviderID == b.ProviderID && a.Provider == b.Provider && a.ProviderKind == b.ProviderKind && a.Profile == b.Profile && a.Model == b.Model && a.Operation == b.Operation && a.RunID == b.RunID && a.PassID == b.PassID
}
func appendEvent(ctx context.Context, tx *sql.Tx, e Event, now time.Time) error {
	if e.StartedAt.Before(now.Add(-RetentionDays * 24 * time.Hour)) {
		return nil
	}
	previous, err := scanEvent(tx.QueryRowContext(ctx, "SELECT "+eventColumns+" FROM api_requests WHERE request_id=?", e.ID))
	if err == nil {
		if !sameRequest(previous, e) {
			return errors.New("statistics contain conflicting request identities")
		}
		var samePayload bool
		if err := tx.QueryRowContext(ctx, "SELECT request_url=? AND request_payload IS ? FROM api_requests WHERE request_id=?", e.RequestURL, e.RequestPayload, e.ID).Scan(&samePayload); err != nil || !samePayload {
			return errors.New("statistics contain conflicting request payloads")
		}
		if previous.Kind == "finish" {
			if e.Kind == "finish" && (previous.Status != e.Status || !reflect.DeepEqual(previous.Counts, e.Counts)) {
				return errors.New("statistics contain conflicting request results")
			}
			if e.Kind == "finish" {
				if err := tx.QueryRowContext(ctx, "SELECT response_payload IS ? AND http_status=? AND response_complete IS ? FROM api_requests WHERE request_id=?", e.ResponsePayload, e.HTTPStatus, e.ResponseComplete, e.ID).Scan(&samePayload); err != nil || !samePayload {
					return errors.New("statistics contain conflicting response payloads")
				}
			}
			return nil
		}
		if e.Kind == "start" {
			return nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return errors.New("cannot read statistics request")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_requests (`+eventColumns+`,`+payloadColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(request_id) DO UPDATE SET kind=excluded.kind,at=excluded.at,status=excluded.status,input_tokens=excluded.input_tokens,output_tokens=excluded.output_tokens,total_tokens=excluded.total_tokens,response_payload=excluded.response_payload,http_status=excluded.http_status,response_complete=excluded.response_complete`, e.ID, e.Kind, stamp(e.At), stamp(e.StartedAt), e.ProviderID, e.Provider, e.Profile, e.Model, e.Operation, e.Status, e.Counts.Input, e.Counts.Output, e.Counts.Total, e.RunID, e.PassID, sql.NullString{String: e.ProviderKind, Valid: e.ProviderKind != ""}, e.RequestURL, e.RequestPayload, e.ResponsePayload, e.HTTPStatus, e.ResponseComplete)
	if err != nil {
		return errors.New("cannot write statistics request")
	}
	return earliest(ctx, tx, "tracking_since", e.StartedAt)
}

// Append records one request event while preserving paired request identity and retention policy.
func (s Store) Append(e Event) error {
	if err := validEvent(e); err != nil {
		return err
	}
	now := s.now()
	if e.StartedAt.Before(now.Add(-RetentionDays * 24 * time.Hour)) {
		return nil
	}
	opened, err := s.open(true, now)
	if err != nil {
		return err
	}
	defer opened.close()
	tx, err := opened.db.BeginTx(opened.ctx, nil)
	if err != nil {
		return errors.New("cannot start statistics write")
	}
	defer func() { _ = tx.Rollback() }()
	if err := appendEvent(opened.ctx, tx, e, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit statistics request")
	}
	return nil
}

func (s Store) Read() ([]Event, bool, error) {
	snapshot, err := s.Snapshot(s.now())
	return snapshot.Events, snapshot.IncompleteTail, err
}

// Snapshot reads retained statistics for reports without changing their event grain.
func (s Store) Snapshot(now time.Time) (Snapshot, error) {
	return s.Window(now, RetentionDays)
}

// Window loads a bounded reporting interval without exposing unrelated retained events.
func (s Store) Window(now time.Time, days int) (Snapshot, error) {
	result := Snapshot{Events: []Event{}, Reviews: []Review{}}
	if days != 7 && days != 30 && days != RetentionDays {
		return result, errors.New("statistics period must be 7d, 30d or 90d")
	}
	if !validTime(now) {
		return result, errors.New("invalid statistics report time")
	}
	now = now.UTC()
	opened, err := s.open(false, now)
	if err != nil || opened == nil {
		return result, err
	}
	defer opened.close()
	tx, err := opened.db.BeginTx(opened.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, errors.New("cannot start statistics read")
	}
	defer func() { _ = tx.Rollback() }()
	since := stamp(now.Add(-time.Duration(days) * 24 * time.Hour))
	if err := func() error {
		rows, err := tx.QueryContext(opened.ctx, "SELECT "+eventColumns+" FROM api_requests WHERE started_at>=? AND started_at<=? ORDER BY request_id", since, stamp(now))
		if err != nil {
			return errors.New("cannot read token statistics")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			e, err := scanEvent(rows)
			if err != nil {
				return errors.New("invalid stored token statistics")
			}
			result.Events = append(result.Events, e)
		}
		if rows.Err() != nil {
			return errors.New("cannot read token statistics")
		}
		return nil
	}(); err != nil {
		return result, err
	}
	if err := func() error {
		rows, err := tx.QueryContext(opened.ctx, "SELECT "+reviewColumns+" FROM review_runs WHERE started_at>=? AND started_at<=? ORDER BY run_id,pass_id", since, stamp(now))
		if err != nil {
			return errors.New("cannot read review statistics")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanReview(rows)
			if err != nil {
				return errors.New("invalid stored review statistics")
			}
			result.Reviews = append(result.Reviews, r)
		}
		if rows.Err() != nil {
			return errors.New("cannot read review statistics")
		}
		return nil
	}(); err != nil {
		return result, err
	}
	for _, field := range []struct {
		key    string
		target **time.Time
	}{{"tracking_since", &result.TrackingSince}, {"review_tracking_since", &result.ReviewTrackingSince}} {
		value, err := meta(opened.ctx, tx, field.key)
		if err != nil {
			return result, errors.New("cannot read statistics metadata")
		}
		if value != "" {
			at, err := parseStamp(value)
			if err != nil {
				return result, errors.New("invalid statistics metadata")
			}
			if !at.After(now) {
				*field.target = &at
			}
		}
	}
	value, err := meta(opened.ctx, tx, "incomplete_legacy_at")
	if err != nil {
		return result, errors.New("cannot read statistics metadata")
	}
	if value != "" {
		at, err := parseStamp(value)
		if err != nil {
			return result, errors.New("invalid statistics metadata")
		}
		result.IncompleteTail = !at.Before(now.Add(-RetentionDays * 24 * time.Hour))
	}
	if err := tx.Commit(); err != nil {
		return result, errors.New("cannot finish statistics read")
	}
	return result, nil
}
