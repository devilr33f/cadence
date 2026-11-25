package tvboard

import (
	"context"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

// the system volume (android): reconciled at start without a raise, then followed and stored; every level in force
// and a mute alone reach the screen's overlay
func TestSystemVolumeFollowedAndShown(t *testing.T) {
	b := newBoard(t)
	b.setup()
	if _, err := b.vol.Set(30, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := screentest.Connect(ctx, b.o.ScreenSocket, screentest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	shown := func(level uint32, muted bool) {
		t.Helper()
		if _, err := s.Wait(2*time.Second, func(e *screenv1.Envelope) bool {
			v := e.GetVolumeState()
			return v.GetShow() && v.GetLevel() == level && v.GetMuted() == muted
		}); err != nil {
			t.Fatalf("no overlay at %d muted %v", level, muted)
		}
	}
	if _, err := s.Wait(2*time.Second, func(e *screenv1.Envelope) bool {
		return e.GetVolumeState().GetLevel() == 30 && !e.GetVolumeState().GetShow()
	}); err != nil {
		t.Fatal("no state on attach")
	}

	// louder system at start: the level stays (audiod then lowers the system to it)
	b.systemVolume(80, false, true)
	if got := b.vol.Level(); got != 30 {
		t.Fatalf("raised to %d", got)
	}
	// quieter system at start: followed down
	b.systemVolume(12, false, true)
	if got := b.vol.Level(); got != 12 {
		t.Fatalf("reconciled = %d", got)
	}
	shown(12, false)
	// the remote's keys android handled: followed, up as well, and stored
	b.systemVolume(47, false, false)
	shown(47, false)
	if level, ok := volume.NewStore(b.o.DataDir).Load(); !ok || level != 47 {
		t.Fatalf("stored %d %v", level, ok)
	}
	// a mute alone moves no level, and shows
	b.systemVolume(47, true, false)
	shown(47, true)
	if got := b.vol.Level(); got != 47 {
		t.Fatalf("mute moved the level to %d", got)
	}
	// a voice or dashboard level carries the mute it has
	if _, err := b.vol.Set(40, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	shown(40, true)
}
