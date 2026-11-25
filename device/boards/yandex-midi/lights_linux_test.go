package midi

import (
	"os"
	"path/filepath"
	"testing"
)

// the real node: a file that is no framebuffer answers no geometry, so nothing is written to it; a missing one
// does not open
func TestRealFramebufferRefusesAFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dev"), 0o750); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(root, "dev", "fb0")
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &lightSink{root: root, open: openFramebuffer}
	if r := reasonOf(s.Ring(pixels(89))); r != "framebuffer_geometry_invalid" {
		t.Fatalf("reason = %q", r)
	}
	if b, err := os.ReadFile(node); err != nil || len(b) != 0 {
		t.Fatalf("fb0 = % x, %v", b, err)
	}
	if err := os.Remove(node); err != nil {
		t.Fatal(err)
	}
	if r := reasonOf(s.Ring(pixels(89))); r != "framebuffer_open_failed" {
		t.Fatalf("reason = %q", r)
	}
}
