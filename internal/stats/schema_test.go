package stats

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestFailedSchemaInitializationCanBeRetried(t *testing.T) {
	s := testStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(s.Path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	previousLimit, err := sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_SQL_LENGTH, 512)
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := initialize(ctx, db, true); err == nil || err.Error() != "cannot create statistics schema" {
		t.Fatalf("schema creation did not fail at the injected SQL limit: %v", err)
	}
	conn, err = db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_SQL_LENGTH, previousLimit)
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(testEvent("after-retry", fixedTime())); err != nil {
		t.Fatalf("schema initialization left an unusable database: %v", err)
	}
	snapshot, err := s.Snapshot(fixedTime())
	if err != nil || len(snapshot.Events) != 1 || snapshot.Events[0].ID != "after-retry" {
		t.Fatalf("retry did not preserve the new request: %+v %v", snapshot, err)
	}
}
