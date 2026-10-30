package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

type Metadata struct{ ProviderID, Provider, Profile, Model string }
type contextKey int

const (
	recorderKey contextKey = iota
	operationKey
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

func Begin(ctx context.Context, meta Metadata) func(Counts, string) {
	r, _ := ctx.Value(recorderKey).(*Recorder)
	if r == nil {
		return func(Counts, string) {}
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return func(Counts, string) {}
	}
	r.pending.Add(1)
	r.mu.Unlock()
	path := r.path
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			r.remember(err)
			r.pending.Done()
			return func(Counts, string) {}
		}
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		r.remember(errors.New("cannot create usage request identity"))
		r.pending.Done()
		return func(Counts, string) {}
	}
	operation, _ := ctx.Value(operationKey).(string)
	if operation == "" {
		operation = "ai"
	}
	now := time.Now().UTC()
	e := Event{Version: 1, ID: hex.EncodeToString(bytes[:]), Kind: "start", At: now, StartedAt: now, ProviderID: meta.ProviderID, Provider: meta.Provider, Profile: meta.Profile, Model: meta.Model, Operation: operation}
	store := Store{Path: path}
	r.remember(store.Append(e))
	var once sync.Once
	return func(counts Counts, status string) {
		once.Do(func() {
			defer r.pending.Done()
			e.Kind = "finish"
			e.At = time.Now().UTC()
			e.Counts = counts
			e.Status = status
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
