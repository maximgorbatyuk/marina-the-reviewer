package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func TestReviewStatisticsCancellationAndFailure(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "canceled"}[canceled], func(t *testing.T) {
			started := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if canceled {
					started <- struct{}{}
					<-r.Context().Done()
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			cfg, route := routeFor(t, server.URL, "")
			plan, err := BuildDocument(document(), route, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
			recorder := usage.NewRecorder(path)
			ctx, cancel := context.WithCancel(usage.WithRecorder(context.Background(), recorder))
			defer cancel()
			dir := filepath.Join(t.TempDir(), "run")
			done := make(chan error, 1)
			go func() { _, err := Run(ctx, plan, cfg, dir); done <- err }()
			if canceled {
				select {
				case <-started:
					cancel()
				case <-time.After(5 * time.Second):
					t.Fatal("request did not start")
				}
			}
			select {
			case err = <-done:
				if err == nil || canceled && !errors.Is(err, context.Canceled) {
					t.Fatalf("wrong execution error: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("review did not finish")
			}
			if err := recorder.Close(); err != nil {
				t.Fatal(err)
			}
			report, err := usage.Generate(usage.Store{Path: path}, time.Now().UTC(), []int{7})
			if err != nil {
				t.Fatal(err)
			}
			r := report.Periods[0].Reviews
			if r.Total != 1 || r.Unfinished != 0 || canceled && r.Canceled != 1 || !canceled && r.Failed != 1 {
				t.Fatalf("wrong review outcome: %+v", r)
			}
		})
	}
}

func TestReviewStatisticsFailureDoesNotFailReview(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { okModel(w) }))
	defer server.Close()
	cfg, route := routeFor(t, server.URL, "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := usage.NewRecorder(t.TempDir())
	result, err := Run(usage.WithRecorder(context.Background(), recorder), plan, cfg, filepath.Join(t.TempDir(), "run"))
	if err != nil || result.Status != "complete" {
		t.Fatalf("statistics error broke review: %v", err)
	}
	if recorder.Close() == nil {
		t.Fatal("statistics failure was hidden")
	}
}

func TestReviewPreflightDoesNotCreateStatistics(t *testing.T) {
	cfg, route := routeFor(t, "http://127.0.0.1:1", "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan.Prepared.GraphSha256 = "invalid"
	path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
	recorder := usage.NewRecorder(path)
	if _, err := Run(usage.WithRecorder(context.Background(), recorder), plan, cfg, filepath.Join(t.TempDir(), "run")); err == nil {
		t.Fatal("invalid plan accepted")
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("preflight created statistics")
	}
}

func TestCompletedLegacyResumeDoesNotFabricateStatistics(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); okModel(w) }))
	defer server.Close()
	cfg, route := routeFor(t, server.URL, "")
	plan, err := BuildDocument(document(), route, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "run")
	if _, err := Run(context.Background(), plan, cfg, dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private", "stats.sqlite3")
	recorder := usage.NewRecorder(path)
	if _, err := Run(usage.WithRecorder(context.Background(), recorder), plan, cfg, dir); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := usage.Generate(usage.Store{Path: path}, time.Now().UTC(), []int{7})
	if err != nil || report.Periods[0].Reviews.Total != 0 || requests.Load() != 1 {
		t.Fatalf("legacy resume created statistics: %+v, %v", report, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("no-work resume created a database")
	}
}
