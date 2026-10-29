package runstore

import (
	"bytes"
	"os"
	"path/filepath"
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
	os.WriteFile(p, data, 0600)
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
	os.Mkdir(public, 0755)
	if _, err := Open(public, "hash"); err == nil {
		t.Fatal("nonprivate run directory accepted")
	}
}
func TestAtomicJSONRefusesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("keep"), 0600)
	link := filepath.Join(dir, "link")
	os.Symlink(target, link)
	if err := AtomicJSON(link, map[string]int{"x": 1}); err == nil {
		t.Fatal("symlink output accepted")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "keep" {
		t.Fatal("changed symlink target")
	}
}
