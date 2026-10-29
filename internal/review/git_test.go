package review

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareClassifiesExternalAndBinaryChanges(t *testing.T) {
	d := t.TempDir()
	gitTest(t, d, "init", "-q")
	for name, content := range map[string]string{"old.txt": "remove me\n", "rename.txt": "same content\n", "binary.dat": "old\x00data", "large.txt": "old\n"} {
		if e := os.WriteFile(filepath.Join(d, name), []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	gitTest(t, d, "add", ".")
	gitTest(t, d, "commit", "-qm", "base")
	base := gitTest(t, d, "rev-parse", "HEAD")
	os.Remove(filepath.Join(d, "old.txt"))
	os.Rename(filepath.Join(d, "rename.txt"), filepath.Join(d, "renamed.txt"))
	os.WriteFile(filepath.Join(d, "binary.dat"), []byte("new\x00data"), 0600)
	os.WriteFile(filepath.Join(d, "pointer"), []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 123\n"), 0600)
	os.WriteFile(filepath.Join(d, ".gitattributes"), []byte("attr.bin binary\n"), 0600)
	os.WriteFile(filepath.Join(d, "attr.bin"), []byte("looks like text\n"), 0600)
	gitTest(t, d, "add", ".")
	gitTest(t, d, "update-index", "--add", "--cacheinfo", "160000,"+base+",external")
	gitTest(t, d, "commit", "-qm", "head")
	s, e := Prepare(context.Background(), PrepareOptions{Repository: d, Base: base, ExactBase: true})
	if e != nil {
		t.Fatal(e)
	}
	found := map[string]FileSnapshot{}
	for _, f := range s.Files {
		found[f.NewPath] = f
	}
	if !found["binary.dat"].IsBinary || !found["attr.bin"].IsBinary || !found["pointer"].IsLFS || !found["external"].IsSubmodule {
		t.Fatalf("classification missing: %+v", found)
	}
	if found["renamed.txt"].Operation != "R" || found["old.txt"].Head != nil {
		t.Fatal("rename or deletion lost")
	}
	doc, e := Analyze(s)
	if e != nil || s.IsComplete || doc.Coverage.IsComplete {
		t.Fatal("unavailable source was reported as complete")
	}
}
func TestPrepareUsesMergeBaseAndIgnoresAmbientRepository(t *testing.T) {
	d := t.TempDir()
	gitTest(t, d, "init", "-q")
	os.WriteFile(filepath.Join(d, "a"), []byte("base\n"), 0600)
	gitTest(t, d, "add", ".")
	gitTest(t, d, "commit", "-qm", "base")
	common := gitTest(t, d, "rev-parse", "HEAD")
	gitTest(t, d, "checkout", "-qb", "branch-a")
	os.WriteFile(filepath.Join(d, "a"), []byte("a\n"), 0600)
	gitTest(t, d, "commit", "-qam", "a")
	a := gitTest(t, d, "rev-parse", "HEAD")
	gitTest(t, d, "checkout", "-qb", "branch-b", common)
	os.WriteFile(filepath.Join(d, "a"), []byte("b\n"), 0600)
	gitTest(t, d, "commit", "-qam", "b")
	b := gitTest(t, d, "rev-parse", "HEAD")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing"))
	s, e := Prepare(context.Background(), PrepareOptions{Repository: d, Base: a, Head: b})
	if e != nil {
		t.Fatal(e)
	}
	if s.BaseSha != common || s.StartSha != a || s.HeadSha != b {
		t.Fatalf("refs not pinned: %+v", s)
	}
}
func TestPrepareDoesNotRunExternalDiffOrTextconv(t *testing.T) {
	d := t.TempDir()
	gitTest(t, d, "init", "-q")
	os.WriteFile(filepath.Join(d, "a.txt"), []byte("base\n"), 0600)
	gitTest(t, d, "add", ".")
	gitTest(t, d, "commit", "-qm", "base")
	base := gitTest(t, d, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(d, "a.txt"), []byte("head\n"), 0600)
	gitTest(t, d, "commit", "-qam", "head")
	sentinel := filepath.Join(d, "executed")
	script := filepath.Join(d, "dangerous-diff")
	os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+sentinel+"'\nprintf altered\n"), 0700)
	gitTest(t, d, "config", "diff.external", script)
	gitTest(t, d, "config", "diff.test.textconv", script)
	os.WriteFile(filepath.Join(d, ".git", "info", "attributes"), []byte("*.txt diff=test\n"), 0600)
	s, e := Prepare(context.Background(), PrepareOptions{Repository: d, Base: base, ExactBase: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(sentinel); !os.IsNotExist(e) {
		t.Fatal("external command ran")
	}
	if len(s.Files) != 1 || s.Files[0].Head.Content != "head\n" {
		t.Fatal("source was transformed")
	}
}
func TestPrepareIgnoresDirtyAndPrivateAttributes(t *testing.T) {
	d := t.TempDir()
	gitTest(t, d, "init", "-q")
	os.WriteFile(filepath.Join(d, "a.txt"), []byte("base\n"), 0600)
	os.WriteFile(filepath.Join(d, ".gitattributes"), []byte("*.txt diff\n"), 0600)
	gitTest(t, d, "add", ".")
	gitTest(t, d, "commit", "-qm", "base")
	base := gitTest(t, d, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(d, "a.txt"), []byte("head\n"), 0600)
	gitTest(t, d, "commit", "-qam", "head")
	clean, e := Prepare(context.Background(), PrepareOptions{Repository: d, Base: base, ExactBase: true})
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(d, ".gitattributes"), []byte("*.txt -diff\n"), 0600)
	os.WriteFile(filepath.Join(d, ".git", "info", "attributes"), []byte("*.txt -diff\n"), 0600)
	dirty, e := Prepare(context.Background(), PrepareOptions{Repository: d, Base: base, ExactBase: true})
	if e != nil {
		t.Fatal(e)
	}
	if clean.ManifestSha256 != dirty.ManifestSha256 {
		t.Fatal("dirty attributes changed the pinned review snapshot")
	}
}
