package stats

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func TestProviderKindIsImmutableAcrossRequestLifecycle(t *testing.T) {
	for _, kind := range []string{"", "xiaomi-mimo"} {
		t.Run("kind="+kind, func(t *testing.T) {
			s := testStore(t)
			start := testEvent("one", fixedTime())
			start.ProviderKind = kind
			if err := s.Append(start); err != nil {
				t.Fatal(err)
			}
			finish := start
			finish.Kind, finish.Status = "finish", "success"
			changed := finish
			changed.ProviderKind = "deepseek"
			if err := s.Append(changed); err == nil {
				t.Fatal("finish changed the selected provider of a started request")
			}
			for range 2 {
				if err := s.Append(finish); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Append(start); err != nil {
				t.Fatal("identical late start was rejected")
			}
			if err := s.Append(changed); err == nil {
				t.Fatal("duplicate finish changed the selected provider")
			}
			snapshot, err := s.Snapshot(fixedTime())
			if err != nil || len(snapshot.Events) != 1 || snapshot.Events[0].ProviderKind != kind || snapshot.Events[0].Kind != "finish" {
				t.Fatalf("selected kind was lost or modified: %+v %v", snapshot, err)
			}
			db, err := sql.Open("sqlite", s.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			var saved sql.NullString
			if err := db.QueryRowContext(t.Context(), "SELECT provider_kind FROM api_requests WHERE request_id='one'").Scan(&saved); err != nil || saved.String != kind || saved.Valid != (kind != "") {
				t.Fatal("unknown provider kind did not remain SQL NULL")
			}
			encoded, err := json.Marshal(snapshot.Events[0])
			if err != nil || strings.Contains(string(encoded), "providerKind") != (kind != "") {
				t.Fatal("optional provider kind changed legacy JSON metadata")
			}
		})
	}
}
