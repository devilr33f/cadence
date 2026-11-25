package tvboard

import (
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

// a screen with audiod plays a group's stream there, so it reports media.group; one without audiod has nowhere to
func TestGroupFollowsAudiod(t *testing.T) {
	has := func(b *Board) bool {
		for _, p := range b.Capabilities() {
			if p.Descriptor().ID == capschema.MediaGroupID {
				return true
			}
		}
		return false
	}
	if !has(newBoard(t)) {
		t.Fatal("no media.group beside audiod")
	}
	b := New(Options{ID: "sim-tv", ScreenSocket: t.TempDir() + "/s.sock"})
	t.Cleanup(func() { _ = b.Close() })
	if has(b) {
		t.Fatal("media.group without audiod")
	}
}
