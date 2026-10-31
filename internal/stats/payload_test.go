package stats

import (
	"bytes"
	"database/sql"
	"testing"
	"time"
)

func TestPayloadLifecycleRejectsConflictsAndExpiresWithRequest(t *testing.T) {
	now := fixedTime()
	s := testStore(t)
	s.Now = func() time.Time { return now }
	e := testEvent("old", now.Add(-90*24*time.Hour))
	e.RequestURL, e.RequestPayload = "https://example.test/v1/chat/completions", []byte("full request")
	if err := s.Append(e); err != nil {
		t.Fatal(err)
	}
	e.Kind, e.Status, e.HTTPStatus, e.ResponsePayload = "finish", "invalid_output", 200, []byte{0xff, 0x00, 'x'}
	complete := true
	e.ResponseComplete = &complete
	for range 2 {
		if err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*Event){
		func(e *Event) { e.RequestURL += "/changed" },
		func(e *Event) { e.RequestPayload = []byte("changed request") },
		func(e *Event) { e.ResponsePayload = []byte("changed response") },
		func(e *Event) { e.HTTPStatus = 500 },
		func(e *Event) { e.ResponseComplete = nil },
	} {
		conflict := e
		mutate(&conflict)
		if err := s.Append(conflict); err == nil {
			t.Fatal("conflicting exchange overwrote the saved request")
		}
	}
	snapshot, err := s.Snapshot(now)
	if err != nil || len(snapshot.Events) != 1 || snapshot.Events[0].RequestPayload != nil || snapshot.Events[0].ResponsePayload != nil {
		t.Fatal("statistics loaded request bodies")
	}
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var response []byte
	if err := db.QueryRow("SELECT response_payload FROM api_requests WHERE request_id='old'").Scan(&response); err != nil || !bytes.Equal(response, e.ResponsePayload) {
		t.Fatal("saved response was changed")
	}
	recent := e
	recent.ID, recent.At, recent.StartedAt = "recent", now, now
	if err := s.Append(recent); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	if _, err := s.Snapshot(now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM api_requests WHERE request_id='old'").Scan(&count); err != nil || count != 0 {
		t.Fatal("expired payload was retained")
	}
	if err := db.QueryRow("SELECT response_payload FROM api_requests WHERE request_id='recent'").Scan(&response); err != nil || !bytes.Equal(response, recent.ResponsePayload) {
		t.Fatal("recent payload was removed")
	}
}
