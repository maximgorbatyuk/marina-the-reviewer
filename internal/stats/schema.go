package stats

import (
	"context"
	"database/sql"
	"errors"
)

const applicationID = 1296323158
const schemaVersion = 3

func supportedSchemaVersion(version int) bool { return version >= 1 && version <= schemaVersion }

const payloadColumns = "request_url,request_payload,response_payload,http_status,response_complete"

const payloadMigration = `
ALTER TABLE api_requests ADD COLUMN request_url TEXT NOT NULL DEFAULT '';
ALTER TABLE api_requests ADD COLUMN request_payload BLOB;
ALTER TABLE api_requests ADD COLUMN response_payload BLOB;
ALTER TABLE api_requests ADD COLUMN http_status INTEGER NOT NULL DEFAULT 0 CHECK(http_status=0 OR http_status BETWEEN 100 AND 599);
ALTER TABLE api_requests ADD COLUMN response_complete INTEGER CHECK(response_complete IN (0,1));
PRAGMA user_version=2;
`

const providerKindMigration = `
ALTER TABLE api_requests ADD COLUMN provider_kind TEXT;
PRAGMA user_version=3;
`

const schema = `
CREATE TABLE api_requests (
 request_id TEXT PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind IN ('start','finish')),
 at TEXT NOT NULL,
 started_at TEXT NOT NULL,
 provider_id TEXT NOT NULL,
 provider TEXT NOT NULL,
 provider_kind TEXT,
 profile TEXT NOT NULL,
 model TEXT NOT NULL,
 operation TEXT NOT NULL,
 status TEXT NOT NULL,
 input_tokens INTEGER CHECK(input_tokens >= 0),
 output_tokens INTEGER CHECK(output_tokens >= 0),
 total_tokens INTEGER CHECK(total_tokens >= 0),
 run_id TEXT NOT NULL,
 pass_id TEXT NOT NULL,
 request_url TEXT NOT NULL DEFAULT '',
 request_payload BLOB,
 response_payload BLOB,
 http_status INTEGER NOT NULL DEFAULT 0 CHECK(http_status=0 OR http_status BETWEEN 100 AND 599),
 response_complete INTEGER CHECK(response_complete IN (0,1))
);
CREATE INDEX api_requests_started ON api_requests(started_at);
CREATE INDEX api_requests_provider_started ON api_requests(provider_id, started_at);
CREATE INDEX api_requests_review ON api_requests(run_id, pass_id);
CREATE TABLE review_runs (
 run_id TEXT NOT NULL,
 pass_id TEXT NOT NULL,
 plan_hash TEXT NOT NULL,
 started_at TEXT NOT NULL,
 finished_at TEXT,
 status TEXT NOT NULL CHECK(status IN ('unfinished','complete','partial','failed','canceled')),
 checked_blocks INTEGER NOT NULL CHECK(checked_blocks >= 0),
 warning_findings INTEGER NOT NULL CHECK(warning_findings >= 0),
 error_findings INTEGER NOT NULL CHECK(error_findings >= 0),
 critical_findings INTEGER NOT NULL CHECK(critical_findings >= 0),
 PRIMARY KEY(run_id, pass_id)
);
CREATE INDEX review_runs_started ON review_runs(started_at);
CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
PRAGMA application_id=1296323158;
PRAGMA user_version=3;
`

func initialize(ctx context.Context, db *sql.DB, empty bool) error {
	var id, version, tables int
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return errors.New("cannot read statistics database identity")
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return errors.New("cannot read statistics schema version")
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return errors.New("cannot read statistics schema")
	}
	if empty && id == 0 && version == 0 && tables == 0 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return errors.New("cannot create statistics schema")
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
			return errors.New("cannot configure statistics space reclamation")
		}
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return errors.New("cannot create statistics schema")
		}
		if err := tx.Commit(); err != nil {
			return errors.New("cannot commit statistics schema")
		}
	} else if id != applicationID || !supportedSchemaVersion(version) {
		return errors.New("unsupported or unrelated statistics database")
	}
	queries := []string{"SELECT " + baseEventColumns + " FROM api_requests LIMIT 0", "SELECT " + reviewColumns + " FROM review_runs LIMIT 0", "SELECT key,value FROM metadata LIMIT 0"}
	if version >= 2 {
		queries = append(queries, "SELECT "+payloadColumns+" FROM api_requests LIMIT 0")
	}
	for _, query := range queries {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return errors.New("statistics schema does not match its version")
		}
		rows.Close()
	}
	if version == 1 || version == 2 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return errors.New("cannot start statistics schema migration")
		}
		defer tx.Rollback()
		if version == 1 {
			if _, err := tx.ExecContext(ctx, payloadMigration); err != nil {
				return errors.New("cannot migrate statistics request payloads")
			}
		}
		if _, err := tx.ExecContext(ctx, providerKindMigration); err != nil {
			return errors.New("cannot migrate statistics provider kinds")
		}
		if err := tx.Commit(); err != nil {
			return errors.New("cannot commit statistics schema migration")
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT "+eventColumns+","+payloadColumns+" FROM api_requests LIMIT 0")
	if err != nil {
		return errors.New("statistics request schema does not match its version")
	}
	rows.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=2000; PRAGMA wal_autocheckpoint=256"); err != nil {
		return errors.New("cannot configure statistics database")
	}
	return nil
}
