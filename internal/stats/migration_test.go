package stats

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const historicalRequestColumns = "request_id,kind,at,started_at,provider_id,provider,profile,model,operation,status,input_tokens,output_tokens,total_tokens,run_id,pass_id"

func historicalDatabase(t *testing.T, s Store, version int) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fixture, err := os.ReadFile(fmt.Sprintf("testdata/schema-v%d.sql", version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(fixture)); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestVersionOneMigrationPreservesHistory(t *testing.T) {
	s := testStore(t)
	db := historicalDatabase(t, s, 1)
	if _, err := db.Exec("INSERT INTO api_requests ("+historicalRequestColumns+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", "old", "finish", stamp(fixedTime()), stamp(fixedTime()), "provider-id", "Example", "cloud", "old-model", "review", "success", 4, 2, 6, "run", "pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO review_runs VALUES(?,?,?,?,?,?,?,?,?,?)", "run", "pass", "plan", stamp(fixedTime()), nil, "unfinished", 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO metadata VALUES('tracking_since',?)", stamp(fixedTime())); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(fixedTime())
	if err != nil || len(snapshot.Events) != 1 || snapshot.Events[0].Model != "old-model" || *snapshot.Events[0].Counts.Total != 6 || len(snapshot.Reviews) != 1 || snapshot.TrackingSince == nil {
		t.Fatalf("history changed during migration: %+v %v", snapshot, err)
	}
	var version, status int
	var url string
	var request, response []byte
	var complete sql.NullBool
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("schema was not upgraded: %d %v", version, err)
	}
	if err := db.QueryRow("SELECT request_url,request_payload,response_payload,http_status,response_complete FROM api_requests WHERE request_id='old'").Scan(&url, &request, &response, &status, &complete); err != nil || url != "" || request != nil || response != nil || status != 0 || complete.Valid {
		t.Fatalf("old payload data was invented: %v", err)
	}
	if err := s.Append(testEvent("new", fixedTime())); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := s.Snapshot(fixedTime()); err != nil || len(snapshot.Events) != 2 {
		t.Fatal("migrated database cannot be reused")
	}
}

func TestVersionOneMigrationRollsBackAndCanBeRetried(t *testing.T) {
	s := testStore(t)
	db := historicalDatabase(t, s, 1)
	if _, err := db.Exec("ALTER TABLE api_requests ADD COLUMN response_payload BLOB"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(fixedTime()); err == nil {
		t.Fatal("conflicting migration succeeded")
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("failed migration changed the schema version")
	}
	if rows, err := db.Query("SELECT request_url FROM api_requests"); err == nil {
		rows.Close()
		t.Fatal("failed migration left partial columns")
	}
	if _, err := db.Exec("ALTER TABLE api_requests DROP COLUMN response_payload"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(fixedTime()); err != nil {
		t.Fatalf("migration cannot be retried: %v", err)
	}
}
