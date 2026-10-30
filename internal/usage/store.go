package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const maxRecordBytes = 8192
const maxLedgerBytes = 128 << 20

type Counts struct {
	Input  *int64 `json:"inputTokens"`
	Output *int64 `json:"outputTokens"`
	Total  *int64 `json:"totalTokens"`
}

type Event struct {
	Version    int       `json:"version"`
	ID         string    `json:"requestId"`
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`
	StartedAt  time.Time `json:"startedAt"`
	ProviderID string    `json:"providerId"`
	Provider   string    `json:"provider"`
	Profile    string    `json:"profile"`
	Model      string    `json:"model"`
	Operation  string    `json:"operation"`
	Status     string    `json:"status,omitempty"`
	Counts     Counts    `json:"usage"`
}

type Store struct{ Path string }

func DefaultPath() (string, error) {
	if path := os.Getenv("MARINADOREVIEW_USAGE_FILE"); path != "" {
		return filepath.Abs(path)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("cannot locate usage directory")
	}
	return filepath.Join(dir, "marina-the-reviewer", "usage.jsonl"), nil
}

func validEvent(e Event) error {
	if e.Version != 1 || (e.Kind != "start" && e.Kind != "finish") || e.ID == "" || e.At.IsZero() || e.StartedAt.IsZero() || e.Kind == "finish" && e.Status == "" {
		return errors.New("invalid usage event format")
	}
	for _, value := range []string{e.ID, e.ProviderID, e.Provider, e.Profile, e.Model, e.Operation} {
		if value == "" || len(value) > 2048 || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
			return errors.New("invalid usage event metadata")
		}
	}
	for _, n := range []*int64{e.Counts.Input, e.Counts.Output, e.Counts.Total} {
		if n != nil && *n < 0 {
			return errors.New("invalid usage token counter")
		}
	}
	return nil
}

func decodeEvent(data []byte) (Event, error) {
	var e Event
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		return e, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return e, errors.New("invalid usage event")
	}
	return e, validEvent(e)
}

func (s Store) open(write bool) (*os.File, error) {
	if write {
		if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
			return nil, errors.New("cannot create usage directory")
		}
	}
	info, err := os.Lstat(s.Path)
	if !write && os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, errors.New("cannot inspect usage journal")
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("usage journal must be a regular file")
	}
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if write {
		flags = os.O_RDWR | os.O_CREATE | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	}
	f, err := os.OpenFile(s.Path, flags, 0600)
	if err != nil {
		return nil, errors.New("cannot open usage journal")
	}
	actual, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, errors.New("cannot inspect usage journal")
	}
	stat, ok := actual.Sys().(*syscall.Stat_t)
	if !actual.Mode().IsRegular() || actual.Mode().Perm()&0077 != 0 || !ok || stat.Uid != uint32(os.Geteuid()) || actual.Size() > maxLedgerBytes {
		f.Close()
		return nil, errors.New("usage journal must be a private owned file (0600), at most 128 MiB")
	}
	lock := syscall.LOCK_SH
	if write {
		lock = syscall.LOCK_EX
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), lock|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, errors.New("cannot lock usage journal")
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, errors.New("usage journal is busy")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func closeFile(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }

func repairTail(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return errors.New("cannot inspect usage journal")
	}
	if info.Size() == 0 {
		return nil
	}
	offset := max(int64(0), info.Size()-maxRecordBytes-1)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return errors.New("cannot read usage journal tail")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil || len(data) == 0 {
		return errors.New("cannot read usage journal tail")
	}
	if data[len(data)-1] == '\n' {
		return nil
	}
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 && offset > 0 {
		return errors.New("usage journal tail exceeds the record size limit")
	}
	fragment := data[last+1:]
	if _, err := decodeEvent(fragment); err == nil {
		if info.Size() >= maxLedgerBytes {
			return errors.New("usage journal has reached 128 MiB")
		}
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return errors.New("cannot seek usage journal")
		}
		_, err = f.WriteString("\n")
		return err
	} else if !errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("usage journal has a corrupt final record")
	}
	if err := f.Truncate(offset + int64(last+1)); err != nil {
		return errors.New("cannot repair incomplete usage journal tail")
	}
	return nil
}

func (s Store) Append(e Event) error {
	if err := validEvent(e); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil || len(line)+1 > maxRecordBytes {
		return errors.New("usage event exceeds the record size limit")
	}
	f, err := s.open(true)
	if err != nil {
		return err
	}
	defer closeFile(f)
	if err := repairTail(f); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return errors.New("cannot inspect usage journal")
	}
	if info.Size()+int64(len(line)+1) > maxLedgerBytes {
		return errors.New("usage journal has reached 128 MiB")
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return errors.New("cannot seek usage journal")
	}
	line = append(line, '\n')
	if n, err := f.Write(line); err != nil || n != len(line) {
		return errors.New("cannot write usage event")
	}
	if err := f.Sync(); err != nil {
		return errors.New("cannot sync usage event")
	}
	return nil
}

func (s Store) Read() ([]Event, bool, error) {
	f, err := s.open(false)
	if err != nil || f == nil {
		return nil, false, err
	}
	defer closeFile(f)
	r := bufio.NewReaderSize(io.LimitReader(f, maxLedgerBytes+1), maxRecordBytes)
	events := []Event{}
	read := 0
	for {
		line, readErr := r.ReadSlice('\n')
		read += len(line)
		if read > maxLedgerBytes {
			return nil, false, errors.New("usage journal exceeds 128 MiB")
		}
		if readErr == io.EOF && len(line) == 0 {
			return events, false, nil
		}
		if readErr != nil && readErr != io.EOF {
			return nil, false, errors.New("usage journal record exceeds size limit or cannot be read")
		}
		e, err := decodeEvent(line)
		if err != nil {
			if readErr == io.EOF && errors.Is(err, io.ErrUnexpectedEOF) {
				return events, true, nil
			}
			return nil, false, errors.New("usage journal contains an invalid or unsupported event")
		}
		events = append(events, e)
		if readErr == io.EOF {
			return events, false, nil
		}
	}
}
