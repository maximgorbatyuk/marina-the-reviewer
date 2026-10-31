package engine

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/runstore"
)

func (b *BundleStore) ExportPublic(destination string) error {
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("public bundle export requires a new destination directory")
	}
	plan, err := b.LoadPlan()
	if err != nil {
		return err
	}
	if _, err = b.PublicArchive(plan); err != nil {
		return err
	}
	if _, err = b.Snapshot(); err != nil {
		return err
	}
	for _, ref := range b.Manifest.Entries {
		reader, err := b.Content.Reader(ref)
		if err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, reader)
		closeErr := reader.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	target, err := runstore.NewContentStoreWithLimits(destination, b.Content.Limits)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(b.Directory, "content"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		raw, err := runstore.ReadPrivate(filepath.Join(b.Directory, "content", entry.Name()), runstore.MaximumContentPartBytes)
		if err != nil {
			return err
		}
		if entry.Name() != review.Hash(string(raw))+".bin" {
			return errors.New("public bundle contains a corrupt part")
		}
		if _, err = target.Put(bytes.NewReader(raw)); err != nil {
			return err
		}
	}
	planBytes, err := b.PlanBytes()
	if err != nil {
		return err
	}
	if err = runstore.AtomicBytes(filepath.Join(destination, "plan.json"), planBytes); err != nil {
		return err
	}
	manifest := b.Manifest
	exporter := Identity()
	manifest.Exporter = &exporter
	return runstore.AtomicJSON(filepath.Join(destination, "bundle.json"), manifest)
}
