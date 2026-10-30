package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func fileRevision(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("cannot inspect configuration file")
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", errors.New("configuration must be a regular file of at most 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot read configuration file")
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, (1<<20)+1)); err != nil {
		return "", errors.New("cannot read configuration file")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func OpenForEdit(path string) (Config, string, error) {
	revision, err := fileRevision(path)
	if err != nil {
		return Config{}, "", err
	}
	if revision == "" {
		return Config{Version: 1, Profiles: map[string]Profile{}, Routing: Routing{FallbackOn: DefaultFallbackErrors()}}, "", nil
	}
	cfg, err := Load(path)
	if err != nil {
		return Config{}, "", err
	}
	current, err := fileRevision(path)
	if err != nil || current != revision {
		return Config{}, "", errors.New("configuration changed while reading; reload it")
	}
	return cfg, revision, nil
}

func Save(path string, cfg Config, expectedRevision string) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	_, current, err := OpenForEdit(path)
	if err != nil {
		return "", err
	}
	if current != expectedRevision {
		return "", errors.New("configuration changed on disk; reload before saving")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", errors.New("cannot encode configuration")
	}
	data = append(data, '\n')
	if len(data) > 1<<20 {
		return "", errors.New("configuration exceeds 1 MiB")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", errors.New("cannot create configuration directory")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".marinadoreview-config-*")
	if err != nil {
		return "", errors.New("cannot create private configuration file")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", errors.New("cannot write configuration")
	}
	if err := f.Sync(); err != nil {
		return "", errors.New("cannot sync configuration")
	}
	if err := f.Close(); err != nil {
		return "", errors.New("cannot close configuration")
	}
	current, err = fileRevision(path)
	if err != nil || current != expectedRevision {
		return "", errors.New("configuration changed on disk; reload before saving")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", errors.New("cannot replace configuration")
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
