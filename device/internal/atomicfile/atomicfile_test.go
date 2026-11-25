package atomicfile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
)

func TestStageAbortRemovesTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, abort, err := atomicfile.Stage(path, []byte("new"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	abort()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want only the old file", len(entries))
	}
	if b, _ := os.ReadFile(path); string(b) != "old" {
		t.Fatalf("content = %q", b)
	}
}

func TestStageCommitReplacesWithMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	commit, _, err := atomicfile.Stage(path, []byte("new"), 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("target exists before commit")
	}
	if err := commit(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatalf("content = %q", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
			t.Fatalf("mode = %v", st.Mode())
		}
	}
}
