package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

type Metadata struct {
	ProviderID, Provider, ProviderKind, Profile, Model string
	RequestURL                                         string
	RequestPayload                                     []byte
}

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

type Recorder struct {
	path    string
	mu      sync.Mutex
	closed  bool
	err     error
	pending sync.WaitGroup
}

func NewRecorder(path string) *Recorder { return &Recorder{path: path} }
func WithRecorder(ctx context.Context, r *Recorder) context.Context {
	return context.WithValue(ctx, recorderKey, r)
}
func WithOperation(ctx context.Context, operation string) context.Context {
	return context.WithValue(ctx, operationKey, operation)
}
func Enabled(ctx context.Context) bool { r, _ := ctx.Value(recorderKey).(*Recorder); return r != nil }

type reviewIdentity struct{ runID, passID string }

func WithReview(ctx context.Context, runID, passID string) context.Context {
	return context.WithValue(ctx, reviewKey, reviewIdentity{runID, passID})
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestKey, requestID)
}

func (r *Recorder) store() (Store, error) {
	if r.path != "" {
		return Store{Path: r.path}, nil
	}
	return DefaultStore()
}

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
