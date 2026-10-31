package runstore

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCorruptStateIsRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	s, e := Open(dir, "plan")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Write(map[string]int{"attempts": 1}); e != nil {
		t.Fatal(e)
	}
	s.Close()
	p := filepath.Join(dir, "state.json")
	data, _ := os.ReadFile(p)
	data = bytes.ReplaceAll(data, []byte(`"attempts": 1`), []byte(`"attempts": 2`))
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Error(err)
	}
	if opened, e := Open(dir, "plan"); e == nil {
		opened.Close()
		t.Fatal("changed state accepted")
	}
}

func TestLockAndPlanIdentityProtectResume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	s, err := Open(dir, "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, "plan-a"); err == nil {
		t.Fatal("second writer acquired lock")
	}
	if err := s.Write(map[string]int{"attempts": 1}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(dir, "plan-b"); err == nil {
		t.Fatal("changed plan accepted")
	}
	resumed, err := Open(dir, "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	var state map[string]int
	if err := resumed.Read(&state); err != nil {
		t.Fatal(err)
	}
	if state["attempts"] != 1 {
		t.Fatal(state)
	}
	st, _ := os.Stat(filepath.Join(dir, "state.json"))
	if st.Mode().Perm() != 0600 {
		t.Fatalf("state mode: %v", st.Mode())
	}
}

func TestWriterLockKeepsOneInodeAcrossResume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	a, e := Open(dir, "plan")
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.Stat(filepath.Join(dir, "writer.lock"))
	if e != nil {
		t.Fatal(e)
	}
	a.Close()
	b, e := Open(dir, "plan")
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	after, e := os.Stat(filepath.Join(dir, "writer.lock"))
	if e != nil {
		t.Fatal(e)
	}
	if !os.SameFile(before, after) {
		t.Fatal("writer lock inode replaced between runs")
	}
}

func TestConcurrentResumeHasOneWriterAfterProcessExit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "writer.lock"), []byte("99999999\nplan"), 0600); e != nil {
		t.Fatal(e)
	}
	var workers sync.WaitGroup
	owners := make(chan *Store, 32)
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		workers.Go(func() {
			<-start
			if s, e := Open(dir, "plan"); e == nil {
				owners <- s
			}
		})
	}
	close(start)
	workers.Wait()
	close(owners)
	count := 0
	for owner := range owners {
		count++
		owner.Close()
	}
	if count != 1 {
		t.Fatalf("concurrent writers: %d", count)
	}
}
func TestSymlinkAndUnrelatedDirectoryAreRejected(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link, "hash"); err == nil {
		t.Fatal("symlink accepted")
	}
	public := filepath.Join(dir, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Error(err)
	}
	if _, err := Open(public, "hash"); err == nil {
		t.Fatal("nonprivate run directory accepted")
	}
}
func TestAtomicJSONRefusesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Error(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Error(err)
	}
	if err := AtomicJSON(link, map[string]int{"x": 1}); err == nil {
		t.Fatal("symlink output accepted")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "keep" {
		t.Fatal("changed symlink target")
	}
}
