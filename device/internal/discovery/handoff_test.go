package discovery

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// the app's responder reads exactly what cadenced's own would answer with, while advertising lasts
func TestAdvertiseHandoffWritesWhileAdvertising(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "discovery.json")
	info := Info{DeviceID: "AB12cd34-ffff", Board: "android-tv", Version: "v9"}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- AdvertiseHandoff(path)(ctx, info, slog.New(slog.NewTextHandler(io.Discard, nil)), func() { close(ready) })
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	var h Handoff
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	n, err := info.names()
	if err != nil {
		t.Fatal(err)
	}
	if h.Name+"."+Service != n.instance.String() {
		t.Fatalf("name %q, the wire's %q", h.Name, n.instance.String())
	}
	want := map[string]string{}
	for _, kv := range info.txt() {
		for i := range kv {
			if kv[i] == '=' {
				want[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	if len(h.TXT) != len(want) {
		t.Fatalf("txt %v, want %v", h.TXT, want)
	}
	for k, v := range want {
		if h.TXT[k] != v {
			t.Fatalf("txt %v, want %v", h.TXT, want)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("handoff left behind: %v", err)
	}
}
