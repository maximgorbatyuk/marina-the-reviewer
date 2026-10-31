package stats

import (
	"bytes"
	"database/sql"
	"fmt"
	"testing"
)

func TestProviderKindMigrationPreservesHistory(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s := testStore(t)
			db := historicalDatabase(t, s, version)
			if _, err := db.ExecContext(t.Context(), "INSERT INTO api_requests ("+historicalRequestColumns+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", "old", "finish", stamp(fixedTime()), stamp(fixedTime()), "provider-id", "Example", "cloud", "old-model", "review", "success", 4, 2, 6, "run", "pass"); err != nil {
				t.Fatal(err)
			}
			request, response := []byte("full request"), []byte{0xff, 0x00, 'x'}
			if version == 2 {
				if _, err := db.ExecContext(t.Context(), "UPDATE api_requests SET request_url=?,request_payload=?,response_payload=?,http_status=200,response_complete=1", "https://example.test/v1/chat/completions", request, response); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Snapshot(fixedTime()); err != nil {
				t.Fatal(err)
			}
			var current int
			if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&current); err != nil || current != 3 {
				t.Fatalf("schema was not upgraded to version 3: %d %v", current, err)
			}
			var kind sql.NullString
			var savedRequest, savedResponse []byte
			if err := db.QueryRowContext(t.Context(), "SELECT provider_kind,request_payload,response_payload FROM api_requests WHERE request_id='old'").Scan(&kind, &savedRequest, &savedResponse); err != nil || kind.Valid {
				t.Fatalf("historical provider selection was invented: %v", err)
			}
			if version == 2 && (!bytes.Equal(savedRequest, request) || !bytes.Equal(savedResponse, response)) {
				t.Fatal("migration changed saved payload bytes")
			}
			if version == 1 && (savedRequest != nil || savedResponse != nil) {
				t.Fatal("migration invented payloads")
			}
		})
	}
}

func TestProviderKindMigrationRollsBackAndCanBeRetried(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s := testStore(t)
			db := historicalDatabase(t, s, version)
			if _, err := db.ExecContext(t.Context(), "ALTER TABLE api_requests ADD COLUMN provider_kind TEXT"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Snapshot(fixedTime()); err == nil {
				t.Fatal("conflicting migration succeeded")
			}
			var current int
			if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&current); err != nil || current != version {
				t.Fatal("failed migration changed the schema version")
			}
			if version == 1 {
				if rows, err := db.QueryContext(t.Context(), "SELECT request_url FROM api_requests"); err == nil {
					defer func() { _ = rows.Close() }()
					t.Fatalf("failed migration left payload columns: %v", rows.Err())
				}
			}
			if _, err := db.ExecContext(t.Context(), "ALTER TABLE api_requests DROP COLUMN provider_kind"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Snapshot(fixedTime()); err != nil {
				t.Fatalf("migration cannot be retried: %v", err)
			}
		})
	}
}

func TestMalformedVersionTwoSchemaIsNotUpgraded(t *testing.T) {
	s := testStore(t)
	db := historicalDatabase(t, s, 2)
	if _, err := db.ExecContext(t.Context(), "ALTER TABLE api_requests DROP COLUMN response_payload"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(fixedTime()); err == nil {
		t.Fatal("incomplete version 2 schema was accepted")
	}
	var version int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal("invalid schema was modified before validation")
	}
	if rows, err := db.QueryContext(t.Context(), "SELECT provider_kind FROM api_requests"); err == nil {
		defer func() { _ = rows.Close() }()
		t.Fatalf("invalid schema received a provider kind column: %v", rows.Err())
	}
}
