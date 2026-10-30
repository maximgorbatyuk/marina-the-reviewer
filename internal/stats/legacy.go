package stats

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"sort"
	"syscall"
	"time"
)

const maxRecordBytes = 8192
const maxLedgerBytes = 128 << 20

func decodeEvent(data []byte) (Event, error) {
	var e Event
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&e); err != nil {
		return e, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return e, errors.New("invalid usage event")
	}
	return e, validEvent(e)
}

type legacyData struct {
	file   *os.File
	info   os.FileInfo
	events []Event
	tail   bool
	digest string
}

func (l *legacyData) close() { unlockFile(l.file) }

func readLegacy(path string) (*legacyData, error) {
	info, err := checkFile(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxLedgerBytes {
		return nil, errors.New("legacy usage journal exceeds 128 MiB")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot open legacy usage journal")
	}
	fail := func(err error) (*legacyData, error) { f.Close(); return nil, err }
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) || !privateInfo(actual, false) {
		return fail(errors.New("legacy usage journal changed while opening"))
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if (!errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN)) || time.Now().After(deadline) {
			return fail(errors.New("legacy usage journal is busy"))
		}
		time.Sleep(5 * time.Millisecond)
	}
	failLocked := func(err error) (*legacyData, error) { unlockFile(f); return nil, err }
	actual, err = f.Stat()
	if err != nil || actual.Size() > maxLedgerBytes {
		return failLocked(errors.New("legacy usage journal exceeds 128 MiB"))
	}
	current, err := checkFile(path)
	if err != nil || !os.SameFile(current, actual) {
		return failLocked(errors.New("legacy usage journal changed while locking"))
	}
	hash := sha256.New()
	reader := bufio.NewReaderSize(io.TeeReader(io.LimitReader(f, maxLedgerBytes+1), hash), maxRecordBytes)
	requests := map[string]Event{}
	read := 0
	tail := false
	for {
		line, readErr := reader.ReadSlice('\n')
		read += len(line)
		if read > maxLedgerBytes {
			return failLocked(errors.New("legacy usage journal exceeds 128 MiB"))
		}
		if readErr == io.EOF && len(line) == 0 {
			break
		}
		if len(line) > maxRecordBytes || readErr != nil && readErr != io.EOF {
			return failLocked(errors.New("legacy usage record exceeds size limit or cannot be read"))
		}
		e, err := decodeEvent(line)
		if err != nil {
			if readErr == io.EOF && errors.Is(err, io.ErrUnexpectedEOF) && len(requests) > 0 {
				tail = true
				break
			}
			return failLocked(errors.New("legacy usage journal contains an invalid or unsupported event"))
		}
		previous, ok := requests[e.ID]
		if ok {
			if !sameRequest(previous, e) {
				return failLocked(errors.New("legacy usage journal contains conflicting request identities"))
			}
			if previous.Kind == "finish" {
				if e.Kind == "finish" && (e.Status != previous.Status || !reflect.DeepEqual(e.Counts, previous.Counts)) {
					return failLocked(errors.New("legacy usage journal contains conflicting request results"))
				}
				if readErr == io.EOF {
					break
				}
				continue
			}
		}
		requests[e.ID] = e
		if readErr == io.EOF {
			break
		}
	}
	keys := make([]string, 0, len(requests))
	for id := range requests {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	events := make([]Event, 0, len(keys))
	for _, id := range keys {
		events = append(events, requests[id])
	}
	return &legacyData{file: f, info: actual, events: events, tail: tail, digest: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (s Store) importLegacy(opened *session, now time.Time) error {
	if s.LegacyPath == "" {
		return nil
	}
	if s.LegacyPath == s.Path {
		return errors.New("SQLite and legacy usage paths must differ")
	}
	if _, err := os.Lstat(s.LegacyPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return errors.New("cannot inspect legacy usage journal")
	}
	pathHash := sha256.Sum256([]byte(s.LegacyPath))
	key := "legacy_import_" + hex.EncodeToString(pathHash[:])
	previous, err := meta(opened.ctx, opened.db, key)
	if err != nil {
		return errors.New("cannot read usage migration state")
	}
	if previous != "" && !s.RetireLegacy {
		return nil
	}
	legacy, err := readLegacy(s.LegacyPath)
	if err != nil {
		return err
	}
	defer legacy.close()
	if previous != "" && previous != legacy.digest {
		return errors.New("legacy usage journal changed after import; stop old versions before using SQLite statistics")
	}
	if previous == "" {
		tx, err := opened.db.BeginTx(opened.ctx, nil)
		if err != nil {
			return errors.New("cannot start usage migration")
		}
		defer tx.Rollback()
		for _, event := range legacy.events {
			if err := earliest(opened.ctx, tx, "tracking_since", event.StartedAt); err != nil {
				return errors.New("cannot migrate usage metadata")
			}
			if err := appendEvent(opened.ctx, tx, event, now); err != nil {
				return err
			}
		}
		if legacy.tail {
			if err := putMeta(opened.ctx, tx, "incomplete_legacy_at", stamp(now)); err != nil {
				return errors.New("cannot save incomplete migration state")
			}
		}
		if err := putMeta(opened.ctx, tx, key, legacy.digest); err != nil {
			return errors.New("cannot save usage migration state")
		}
		if err := tx.Commit(); err != nil {
			return errors.New("cannot commit usage migration")
		}
	}
	if s.RetireLegacy {
		for _, expected := range legacy.events {
			if expected.StartedAt.Before(now.Add(-RetentionDays * 24 * time.Hour)) {
				continue
			}
			stored, err := scanEvent(opened.db.QueryRowContext(opened.ctx, "SELECT "+eventColumns+" FROM api_requests WHERE request_id=?", expected.ID))
			if err != nil || !sameRequest(expected, stored) || expected.Kind == "finish" && (stored.Kind != "finish" || expected.Status != stored.Status || !reflect.DeepEqual(expected.Counts, stored.Counts)) {
				return errors.New("cannot verify committed usage migration; source was preserved")
			}
		}
		info, err := checkFile(s.LegacyPath)
		if err != nil || !os.SameFile(info, legacy.info) || info.Size() != legacy.info.Size() || !info.ModTime().Equal(legacy.info.ModTime()) {
			return errors.New("legacy usage journal changed after migration; source was preserved")
		}
		if _, err := legacy.file.Seek(0, io.SeekStart); err != nil {
			return errors.New("cannot verify legacy usage migration")
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, io.LimitReader(legacy.file, maxLedgerBytes+1)); err != nil || hex.EncodeToString(hash.Sum(nil)) != legacy.digest {
			return errors.New("legacy usage journal changed after migration; source was preserved")
		}
		if err := os.Remove(s.LegacyPath); err != nil {
			return errors.New("cannot retire migrated legacy usage journal")
		}
	}
	return nil
}
