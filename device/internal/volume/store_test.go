package volume_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := volume.NewStore(dir)
	if _, ok := s.Load(); ok {
		t.Fatal("a missing file loaded as known")
	}
	if err := s.Save(30); err != nil {
		t.Fatal(err)
	}
	if level, ok := volume.NewStore(dir).Load(); !ok || level != 30 {
		t.Fatalf("load = %d, %v", level, ok)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state", "volume"))
	if err != nil || string(raw) != `{"level":30}` {
		t.Fatalf("file = %s, %v", raw, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, "state", "volume"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", fi.Mode(), err)
		}
	}
}

func TestStoreCorruptIsUnknown(t *testing.T) {
	for _, bad := range []string{``, `nope`, `{}`, `{"level":101}`, `{"level":-1}`, `{"level":1.5}`, `{"level":"30"}`, `[30]`} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "state", "volume"), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if level, ok := volume.NewStore(dir).Load(); ok {
			t.Fatalf("%q loaded as %d", bad, level)
		}
	}
}

func TestNilStoreKeepsNothing(t *testing.T) {
	var s *volume.Store
	if err := s.Save(30); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Load(); ok {
		t.Fatal("a nil store loaded a level")
	}
}
