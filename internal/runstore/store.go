package runstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const MaximumDocumentBytes = 64 << 20

type Store struct {
	dir, planHash, lockValue string
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
	s := &Store{dir: dir, planHash: planHash, lockValue: strconv.Itoa(os.Getpid()) + "\n" + planHash}
	lock := filepath.Join(dir, "writer.lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		data, readErr := ReadPrivate(lock, 4096)
		pid, parseErr := strconv.Atoi(strings.SplitN(string(data), "\n", 2)[0])
		if readErr != nil || parseErr != nil || pid <= 1 || syscall.Kill(pid, 0) != syscall.ESRCH {
			return nil, errors.New("run directory is locked by another writer")
		}
		if err = os.Remove(lock); err != nil {
			return nil, err
		}
		f, err = os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	if err != nil {
		return nil, err
	}
	if _, err = f.WriteString(s.lockValue); err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		s.Close()
		return nil, err
	}
	if data, err := ReadPrivate(filepath.Join(dir, "state.json"), MaximumDocumentBytes); err == nil {
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
	path := filepath.Join(s.dir, "writer.lock")
	if value, err := os.ReadFile(path); err == nil && string(value) == s.lockValue {
		_ = os.Remove(path)
	}
}

func (s *Store) Read(target any) error {
	data, err := ReadPrivate(filepath.Join(s.dir, "state.json"), MaximumDocumentBytes)
	if err != nil {
		return err
	}
	var stored envelope
	if json.Unmarshal(data, &stored) != nil || stored.Version != 1 || stored.PlanHash != s.planHash || stored.StateSha256 == "" || stateHash(stored.State) != stored.StateSha256 {
		return errors.New("saved run identity is invalid")
	}
	if json.Unmarshal(stored.State, target) != nil {
		return errors.New("saved run state is invalid")
	}
	return nil
}

func (s *Store) Write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return AtomicJSON(filepath.Join(s.dir, "state.json"), envelope{Version: 1, PlanHash: s.planHash, State: data, StateSha256: stateHash(data)})
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
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if buffer.Len() > MaximumDocumentBytes {
		return errors.New("saved document exceeds the size limit")
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
	if _, err = f.Write(buffer.Bytes()); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
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
