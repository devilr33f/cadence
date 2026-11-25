package syncstore_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/syncstore"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	s, err := syncstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save("audio.output", 7, json.RawMessage(`{"level":30}`)); err != nil {
		t.Fatal(err)
	}
	gen, cfg, ok, err := s.Load("audio.output")
	if err != nil || !ok || gen != 7 || string(cfg) != `{"level":30}` {
		t.Fatalf("gen = %d, cfg = %s, ok = %v, err = %v", gen, cfg, ok, err)
	}
}

func TestLoadMissing(t *testing.T) {
	s, err := syncstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, ok, err := s.Load("audio.output")
	if err != nil || ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
}

func TestRejectsPathLikeID(t *testing.T) {
	s, _ := syncstore.Open(t.TempDir())
	if err := s.Save("../x", 1, json.RawMessage(`{}`)); err == nil {
		t.Fatal("path-like id accepted")
	}
}

func TestSaveAtomic(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a read-only dir that is enforced")
	}
	dir := t.TempDir()
	s, _ := syncstore.Open(dir)
	if err := s.Save("c", 1, json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := s.Save("c", 2, json.RawMessage(`{"a":2}`)); err == nil {
		t.Fatal("save into a read-only dir succeeded")
	}
	gen, cfg, ok, err := s.Load("c")
	if err != nil || !ok || gen != 1 || string(cfg) != `{"a":1}` {
		t.Fatalf("gen = %d, cfg = %s, ok = %v, err = %v", gen, cfg, ok, err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, ".tmp-*")); len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}
}

// the secrets a config came with stay beside it, in the store's own mode-600 file
func TestEntryKeepsSecrets(t *testing.T) {
	dir := t.TempDir()
	s, _ := syncstore.Open(dir)
	in := syncstore.Entry{Generation: 3, Config: json.RawMessage(`{"enabled":true}`), Secrets: map[string]string{"apiKey": "k-1"}}
	if err := s.SaveEntry("media.soloist", in); err != nil {
		t.Fatal(err)
	}
	e, ok, err := s.LoadEntry("media.soloist")
	if err != nil || !ok || e.Generation != 3 || string(e.Config) != `{"enabled":true}` || e.Secrets["apiKey"] != "k-1" || len(e.Secrets) != 1 {
		t.Fatalf("entry = %+v, ok = %v, err = %v", e, ok, err)
	}
	if err := s.Save("media.soloist", 4, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if e, _, _ := s.LoadEntry("media.soloist"); e.Secrets != nil {
		t.Fatalf("a save without secrets kept %d", len(e.Secrets))
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(dir, "media.soloist.json"))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, err = %v", st.Mode(), err)
		}
	}
}

func TestDeviceNameRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, _ := syncstore.Open(dir)
	if name, ok, err := s.LoadDeviceName(); err != nil || ok || name != "" {
		t.Fatalf("empty store: %q %v %v", name, ok, err)
	}
	for _, name := range []string{"Bedroom Station 2", "Кухня", ""} {
		if err := s.SaveDeviceName(name); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := s.LoadDeviceName(); err != nil || !ok || got != name {
			t.Fatalf("%q: %q %v %v", name, got, ok, err)
		}
	}
	// beside the configs, never one of them
	if _, _, ok, err := s.Load("_device"); ok || err == nil {
		t.Fatalf("the device file reads as a capability: ok %v err %v", ok, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "_device.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadDeviceName(); ok || err == nil {
		t.Fatalf("corrupt file: ok %v err %v", ok, err)
	}
}
