// Package usage attributes provider calls to operations without requiring global recording state.
package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Metadata binds request attribution and payload data before an inference operation starts.
type Metadata struct {
	ProviderID, Provider, ProviderKind, Profile, Model string
	RequestURL                                         string
	RequestPayload                                     []byte
}

// Response carries response metadata so usage recording can distinguish failed and incomplete requests.
type Response struct {
	Payload    []byte
	HTTPStatus int
	Complete   *bool
}
type contextKey int

const (
	recorderKey contextKey = iota
	operationKey
	reviewKey
	requestKey
)

// Recorder tracks pending usage writes so shutdown can report persistence failures.
type Recorder struct {
	path    string
	mu      sync.Mutex
	closed  bool
	err     error
	pending sync.WaitGroup
}

// NewRecorder creates a recorder for an explicit statistics path or the platform default.
func NewRecorder(path string) *Recorder { return &Recorder{path: path} }

// WithRecorder attaches usage recording to operations without a global recorder.
func WithRecorder(ctx context.Context, r *Recorder) context.Context {
	return context.WithValue(ctx, recorderKey, r)
}

// WithOperation attributes nested provider requests to the caller's operation.
func WithOperation(ctx context.Context, operation string) context.Context {
	return context.WithValue(ctx, operationKey, operation)
}

// Enabled reports whether the context contains a usage recorder before payload capture is attempted.
func Enabled(ctx context.Context) bool { r, _ := ctx.Value(recorderKey).(*Recorder); return r != nil }

type reviewIdentity struct{ runID, passID string }

// WithReview links provider usage to the review run and pass that caused it.
func WithReview(ctx context.Context, runID, passID string) context.Context {
	return context.WithValue(ctx, reviewKey, reviewIdentity{runID, passID})
}

// WithRequestID lets retries and inference logs share the caller's request identity.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestKey, requestID)
}

func (r *Recorder) store() (Store, error) {
	if r.path != "" {
		return Store{Path: r.path}, nil
	}
	return DefaultStore()
}

// RecordReview records a review outcome without changing the command's execution result.
func RecordReview(ctx context.Context, review Review) {
	r, _ := ctx.Value(recorderKey).(*Recorder)
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.pending.Add(1)
	r.mu.Unlock()
	defer r.pending.Done()
	store, err := r.store()
	if err == nil {
		err = store.SaveReview(review)
	}
	r.remember(err)
}

func (r *Recorder) remember(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

// Begin records request start and returns an idempotent function for recording its finish.
func Begin(ctx context.Context, meta Metadata) func(Counts, string, Response) {
	r, _ := ctx.Value(recorderKey).(*Recorder)
	if r == nil {
		return func(Counts, string, Response) {}
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return func(Counts, string, Response) {}
	}
	r.pending.Add(1)
	r.mu.Unlock()
	store, err := r.store()
	if err != nil {
		r.remember(err)
		r.pending.Done()
		return func(Counts, string, Response) {}
	}
	requestID, _ := ctx.Value(requestKey).(string)
	if requestID == "" {
		var bytes [16]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			r.remember(errors.New("cannot create usage request identity"))
			r.pending.Done()
			return func(Counts, string, Response) {}
		}
		requestID = hex.EncodeToString(bytes[:])
	}
	operation, _ := ctx.Value(operationKey).(string)
	if operation == "" {
		operation = "ai"
	}
	now := time.Now().UTC()
	e := Event{Version: 1, ID: requestID, Kind: "start", At: now, StartedAt: now, ProviderID: meta.ProviderID, Provider: meta.Provider, ProviderKind: meta.ProviderKind, Profile: meta.Profile, Model: meta.Model, Operation: operation}
	e.RequestURL, e.RequestPayload = meta.RequestURL, meta.RequestPayload
	if review, ok := ctx.Value(reviewKey).(reviewIdentity); ok {
		e.RunID, e.PassID = review.runID, review.passID
	}
	r.remember(store.Append(e))
	var once sync.Once
	return func(counts Counts, status string, response Response) {
		once.Do(func() {
			defer r.pending.Done()
			e.Kind = "finish"
			e.At = time.Now().UTC()
			e.Counts = counts
			e.Status = status
			e.ResponsePayload, e.HTTPStatus, e.ResponseComplete = response.Payload, response.HTTPStatus, response.Complete
			r.remember(store.Append(e))
		})
	}
}

// Close waits briefly for pending writes and returns the first recording failure.
func (r *Recorder) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.pending.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		r.remember(errors.New("some usage requests remain unfinished"))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}
