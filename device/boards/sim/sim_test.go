package sim_test

import (
	"context"
	"errors"
	"testing"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/board"
)

func TestFingerprint(t *testing.T) {
	ok := sim.New(func(k string) string {
		if k == "CADENCE_SIM" {
			return "1"
		}
		return ""
	}, "", nil)
	if ok.ID() != "sim" {
		t.Fatalf("id = %q", ok.ID())
	}
	if err := ok.Fingerprint(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := sim.New(func(string) string { return "" }, "", nil)
	if err := bad.Fingerprint(context.Background()); !errors.Is(err, board.ErrFingerprint) {
		t.Fatalf("err = %v", err)
	}
}

// the sim keeps a level core puts back on the hardware, and plays no media without audiod
func TestVolumeAndMediaPlayback(t *testing.T) {
	b := sim.New(func(string) string { return "1" }, t.TempDir(), nil)
	vb, ok := b.(board.Volume)
	if !ok || vb.Volume() == nil {
		t.Fatal("no volume")
	}
	if mp, ok := b.(board.MediaPlayback); !ok || mp.MediaPlaying() {
		t.Fatal("media playing without audiod")
	}
}

func TestTelemetryProfile(t *testing.T) {
	dir := t.TempDir()
	tp, ok := sim.New(func(string) string { return "" }, dir, nil).(board.Telemetry)
	if !ok {
		t.Fatal("sim has no telemetry profile")
	}
	got := tp.Telemetry()
	if got.Kmsg || len(got.Mounts) != 1 || got.Mounts[0] != dir || got.TempSensor != "" || len(got.LogFiles) != 0 {
		t.Fatalf("profile = %+v", got)
	}
	none, _ := sim.New(func(string) string { return "" }, "", nil).(board.Telemetry)
	if len(none.Telemetry().Mounts) != 0 {
		t.Fatal("mounts without a data dir")
	}
}
