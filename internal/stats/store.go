package stats

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite" // Register the SQLite driver used by private statistics storage.
)

// DefaultStore resolves private statistics storage and optional legacy import settings.
func DefaultStore() (Store, error) {
	selected, legacy := os.Getenv("MARINADOREVIEW_DB_FILE"), os.Getenv("MARINADOREVIEW_USAGE_FILE")
	s := Store{}
	if selected != "" {
		s.Path = selected
		s.LegacyPath = legacy
	} else if legacy != "" {
		s.Path, s.LegacyPath = legacy+".sqlite3", legacy
	} else {
		dir, err := os.UserConfigDir()
		if err != nil {
			return s, errors.New("cannot locate statistics directory")
		}
		dir = filepath.Join(dir, "marina-the-reviewer")
		s.Path, s.LegacyPath, s.RetireLegacy = filepath.Join(dir, "stats.sqlite3"), filepath.Join(dir, "usage.jsonl"), true
	}
	var err error
	s.Path, err = filepath.Abs(s.Path)
	if err != nil {
		return Store{}, errors.New("cannot resolve statistics path")
	}
	if s.LegacyPath != "" {
		s.LegacyPath, err = filepath.Abs(s.LegacyPath)
		if err != nil {
			return Store{}, errors.New("cannot resolve legacy usage path")
		}
	}
	if s.Path == s.LegacyPath {
		return Store{}, errors.New("SQLite and legacy usage paths must differ")
	}
	return s, nil
}

// DefaultPath returns the resolved private statistics database path.
func DefaultPath() (string, error) { s, err := DefaultStore(); return s.Path, err }

func privateInfo(info os.FileInfo, directory bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0077 == 0 && ((directory && info.IsDir()) || (!directory && info.Mode().IsRegular()))
}

func checkFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !privateInfo(info, false) {
		return nil, errors.New("statistics files must be private regular files owned by the current user")
	}
	return info, nil
}

func lockFile(path string) (*os.File, error) {
	if _, err := checkFile(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errors.New("cannot open statistics lock")
	}
	info, err := f.Stat()
	if err != nil || !privateInfo(info, false) {
		_ = f.Close()
		return nil, errors.New("statistics lock must be private and owned")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if (!errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN)) || time.Now().After(deadline) {
			_ = f.Close()
			return nil, errors.New("statistics database is busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }

type session struct {
	db     *sql.DB
	ctx    context.Context
	cancel context.CancelFunc
	lock   *os.File
}

func (s *session) close() { _ = s.db.Close(); s.cancel(); unlockFile(s.lock) }

func (s Store) open(write bool, now time.Time) (*session, error) {
	if s.Path == "" {
		return nil, errors.New("statistics database path is required")
	}
	_, err := checkFile(s.Path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	missing := os.IsNotExist(err)
	if missing && !write {
		if s.LegacyPath == "" {
			return nil, nil
		}
		if _, err := os.Lstat(s.LegacyPath); os.IsNotExist(err) {
			return nil, nil
		} else if err != nil {
			return nil, errors.New("cannot inspect legacy usage journal")
		}
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("cannot create statistics directory")
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || !privateInfo(dirInfo, true) {
		return nil, errors.New("statistics directory must be private and owned")
	}
	lock, err := lockFile(s.Path + ".lock")
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*session, error) { unlockFile(lock); return nil, e }
	info, err := checkFile(s.Path)
	if err != nil && !os.IsNotExist(err) {
		return fail(err)
	}
	if os.IsNotExist(err) {
		f, e := os.OpenFile(s.Path, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return fail(errors.New("cannot create statistics database"))
		}
		info, e = f.Stat()
		_ = f.Close()
		if e != nil {
			return fail(errors.New("cannot inspect statistics database"))
		}
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, e := checkFile(s.Path + suffix); e != nil && !os.IsNotExist(e) {
			return fail(e)
		}
	}
	if info.Size() > 0 {
		f, e := os.OpenFile(s.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return fail(errors.New("cannot read statistics database"))
		}
		header := make([]byte, 100)
		_, e = io.ReadFull(f, header)
		_ = f.Close()
		if e != nil || string(header[:16]) != "SQLite format 3\x00" {
			return fail(errors.New("statistics file is not a SQLite database"))
		}
		if binary.BigEndian.Uint32(header[68:72]) != applicationID || !supportedSchemaVersion(int(binary.BigEndian.Uint32(header[60:64]))) {
			return fail(errors.New("unsupported or unrelated statistics database"))
		}
	}
	u := url.URL{Scheme: "file", Path: s.Path}
	q := url.Values{}
	q.Set("mode", "rw")
	q.Set("_pragma", "busy_timeout(2000)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "wal_autocheckpoint(256)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return fail(errors.New("cannot open statistics database"))
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	opened := &session{db: db, ctx: ctx, cancel: cancel, lock: lock}
	if err := initialize(ctx, db, info.Size() == 0); err != nil {
		opened.close()
		return nil, err
	}
	if err := s.importLegacy(opened, now); err != nil {
		opened.close()
		return nil, err
	}
	if err := maintain(opened, now); err != nil {
		opened.close()
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, e := checkFile(s.Path + suffix); e != nil && !os.IsNotExist(e) {
			opened.close()
			return nil, e
		}
	}
	return opened, nil
}

func meta(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (string, error) {
	var value string
	err := db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}
func putMeta(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}
func earliest(ctx context.Context, tx *sql.Tx, key string, at time.Time) error {
	old, err := meta(ctx, tx, key)
	if err != nil {
		return err
	}
	value := stamp(at)
	if old == "" || value < old {
		return putMeta(ctx, tx, key, value)
	}
	return nil
}
