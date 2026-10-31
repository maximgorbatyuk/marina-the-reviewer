package review

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateNativeSnapshotRejectsCorruption(t *testing.T) {
	d := t.TempDir()
	gitTest(t, d, "init", "-q")
	if err := os.WriteFile(filepath.Join(d, "a.go"), []byte("base\n"), 0600); err != nil {
		t.Error(err)
	}
	gitTest(t, d, "add", ".")
	gitTest(t, d, "commit", "-qm", "base")
	base := gitTest(t, d, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(d, "a.go"), []byte("head\n"), 0600); err != nil {
		t.Error(err)
	}
	gitTest(t, d, "commit", "-qam", "head")
	s, e := Prepare(context.Background(), PrepareOptions{Repository: d, Base: base, ExactBase: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = ValidateSnapshot(s); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Snapshot){func(s *Snapshot) { s.Version = 9 }, func(s *Snapshot) { s.Files[0].Head.Content = "bad" }, func(s *Snapshot) { s.Files[0].Head.Path = "other" }, func(s *Snapshot) { s.Files[0].Head.CommitSha = s.BaseSha }, func(s *Snapshot) { s.Files[0].ChangedLines[0].Line = 100 }, func(s *Snapshot) { s.ManifestSha256 = "bad" }, func(s *Snapshot) { s.HeadSha = "main" }} {
		var cloned Snapshot
		if e = json.Unmarshal(marshal(s), &cloned); e != nil {
			t.Fatal(e)
		}
		change(&cloned)
		if e = ValidateSnapshot(cloned); e == nil {
			t.Fatal("corrupt snapshot accepted")
		}
	}
}
