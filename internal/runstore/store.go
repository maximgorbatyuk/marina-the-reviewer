package runstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const MaximumDocumentBytes = 64 << 20
const MaximumExecutionDocumentBytes = 128 << 20

type Store struct {
	dir, planHash string
	lock          *os.File
	codec         Codec
}

type Codec interface {
	Encode(any) ([]byte, error)
	Decode([]byte, any) error
}

func OpenWithCodec(dir, planHash string, codec Codec) (*Store, error) {
	store, err := Open(dir, planHash)
	if err == nil {
		store.codec = codec
	}
	return store, err
}

type envelope struct {
	Version     int             `json:"version"`
	PlanHash    string          `json:"planHash"`
	State       json.RawMessage `json:"state"`
	StateSha256 string          `json:"stateSha256"`
}

func stateHash(raw json.RawMessage) string {
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return ""
	}
	sum := sha256.Sum256(compact.Bytes())
	return hex.EncodeToString(sum[:])
}

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func Open(dir, planHash string) (*Store, error) {
	if planHash == "" {
		return nil, errors.New("run plan hash is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || !owned(info) {
		return nil, errors.New("run directory must be a private regular directory (0700)")
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, planHash: planHash}
	lock := filepath.Join(dir, "writer.lock")
	f, err := os.OpenFile(lock, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !owned(info) {
		f.Close()
		return nil, errors.New("writer lock must be a private regular file")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("run directory is locked by another writer")
	}
	s.lock = f
	if err = f.Truncate(0); err == nil {
		_, err = f.WriteString(strconv.Itoa(os.Getpid()) + "\n" + planHash)
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	if data, err := ReadPrivate(filepath.Join(dir, "state.json"), MaximumExecutionDocumentBytes); err == nil {
		var stored envelope
		if json.Unmarshal(data, &stored) != nil || stored.Version != 1 || stored.PlanHash != planHash || stored.StateSha256 == "" || stateHash(stored.State) != stored.StateSha256 {
			s.Close()
			return nil, errors.New("saved run belongs to another plan or is corrupt")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() {
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
		s.lock = nil
	}
}

func (s *Store) Read(target any) error {
	data, err := ReadPrivate(filepath.Join(s.dir, "state.json"), MaximumExecutionDocumentBytes)
	if err != nil {
		return err
	}
	var stored envelope
	if json.Unmarshal(data, &stored) != nil || stored.Version != 1 || stored.PlanHash != s.planHash || stored.StateSha256 == "" || stateHash(stored.State) != stored.StateSha256 {
		return errors.New("saved run identity is invalid")
	}
	var decodeErr error
	if s.codec == nil {
		decodeErr = json.Unmarshal(stored.State, target)
	} else {
		decodeErr = s.codec.Decode(stored.State, target)
	}
	if decodeErr != nil {
		return fmt.Errorf("saved run state is invalid: %w", decodeErr)
	}
	return nil
}

func (s *Store) Write(value any) error {
	var data []byte
	var err error
	if s.codec == nil {
		data, err = json.Marshal(value)
	} else {
		data, err = s.codec.Encode(value)
	}
	if err != nil {
		return err
	}
	return AtomicJSONLimit(filepath.Join(s.dir, "state.json"), envelope{Version: 1, PlanHash: s.planHash, State: data, StateSha256: stateHash(data)}, MaximumExecutionDocumentBytes)
}

func (s *Store) Event(value any) error {
	path := filepath.Join(s.dir, "events.jsonl")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return errors.New("event path must be a private regular file")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(value); err != nil {
		return err
	}
	return f.Sync()
}

func ReadPrivate(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !owned(info) {
		return nil, errors.New("saved run file must be a private regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("saved run file changed during read")
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if int64(len(data)) > maximum {
		return nil, errors.New("saved run file exceeds the size limit")
	}
	return data, err
}

func ReadBoundedFile(path string, maximum int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err == nil && int64(len(data)) > maximum {
		err = errors.New("document exceeds the size limit")
	}
	return data, err
}

func AtomicJSON(path string, value any) error {
	return AtomicJSONContext(context.Background(), path, value)
}
func AtomicJSONContext(ctx context.Context, path string, value any) error {
	return AtomicJSONLimitContext(ctx, path, value, MaximumDocumentBytes)
}
func AtomicJSONLimit(path string, value any, maximum int) error {
	return AtomicJSONLimitContext(context.Background(), path, value, maximum)
}
func AtomicJSONLimitContext(ctx context.Context, path string, value any, maximum int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if maximum < 1 {
		return errors.New("document size limit must be positive")
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("output must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(parent, ".marinadoreview-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	encode := func(pretty bool) error {
		writer := &boundedArtifactWriter{writer: &contentContextWriter{ctx: ctx, writer: f}, maximum: int64(maximum), artifact: filepath.Base(path)}
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		if pretty {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(value)
	}
	err = encode(true)
	if cancellation := ctx.Err(); cancellation != nil {
		err = cancellation
	}
	var sizeError *ArtifactSizeError
	if errors.As(err, &sizeError) {
		if err = f.Truncate(0); err == nil {
			_, err = f.Seek(0, io.SeekStart)
		}
		if err == nil {
			err = encode(false)
		}
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return fmt.Errorf("output could not be saved: %w", err)
	}
	if directory, err := os.Open(parent); err == nil {
		defer directory.Close()
		if err := directory.Sync(); err != nil {
			return err
		}
	}
	return nil
}

type ArtifactSizeError struct {
	Artifact string
	Actual   int64
	Allowed  int64
}

func (e *ArtifactSizeError) Error() string {
	return fmt.Sprintf("saved document exceeds the size limit: artifact=%s actual=%d allowed=%d bytes", e.Artifact, e.Actual, e.Allowed)
}

type boundedArtifactWriter struct {
	writer   io.Writer
	maximum  int64
	written  int64
	artifact string
}

func (w *boundedArtifactWriter) Write(data []byte) (int, error) {
	if w.written+int64(len(data)) > w.maximum {
		return 0, &ArtifactSizeError{w.artifact, w.written + int64(len(data)), w.maximum}
	}
	n, err := w.writer.Write(data)
	w.written += int64(n)
	return n, err
}
