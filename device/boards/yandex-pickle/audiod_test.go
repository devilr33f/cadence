package pickle_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	pickle "git.shino.boo/devilreef/cadence/device/boards/yandex-pickle"
	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	"git.shino.boo/devilreef/cadence/device/internal/input"
)

func waitRecord(t *testing.T, dir, kind, detail string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, r := range audiodtest.Records(dir) {
			if r.Kind == kind && r.Detail == detail {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s %s in %v", kind, detail, audiodtest.Records(dir))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// audiod runs from the board's runtime dir with its configure, the kept level, the switch's position and the media
// dirs where the board keeps them
func TestAudiod(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("audiod runs on linux only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tr := pickleTree(t)
	tr.inputDevice("event1", "mute")
	tr.write("data/cadence/state/volume", `{"level":25}`)
	tr.write("data/cadence/stock/command/words.txt", "")
	b, logs := newBoard(t, tr, pickle.WithAudiod(exe), pickle.WithSwitch(positions(true)))
	if _, ok := b.AudioIO(); !ok {
		t.Fatal("no audio")
	}
	waitLog(t, logs, "msg=audiod_ready")
	p := pickle.PathsUnder(tr.root)
	if !strings.HasPrefix(p.Socket, tr.root) {
		t.Fatalf("socket %s outside the tree", p.Socket)
	}
	waitRecord(t, p.RuntimeDir, "configure_capture",
		`backend=BACKEND_ALSA device="default" rate=48000 channels=4 period=256 buffer=8192 layout=4/[0 1 2]/[3]/48000`)
	roots := []string{tr.path("usr/lib/cadence-stock"), tr.path("system/vendor/quasar"), tr.path("system/vendor/.quasar-disabled")}
	waitRecord(t, p.RuntimeDir, "configure_engines", fmt.Sprintf("enabled=true roots=%q library=%q activation=%q command=%q preset=%q words=%q",
		roots, "libs/libyandex_evil.so", "spotter_models/activation/alisa", p.CommandModel, "pickle", input.CommandWords()))
	waitRecord(t, p.RuntimeDir, "configure_playback", `backend=BACKEND_ALSA device="default" mixer="Master" period=960 buffer=7680`)
	waitRecord(t, p.RuntimeDir, "configure_output", fmt.Sprintf("channels=2 eq_bands=0 ca_file=%q", p.CABundle))
	waitRecord(t, p.RuntimeDir, "volume_set", "25")
	waitRecord(t, p.RuntimeDir, "mute_state", "true")
	waitRecord(t, p.RuntimeDir, "soloist_config", fmt.Sprintf("enabled=false key_bytes=0 name=%q dir=%q scratch=%q ca=%q initial=25",
		"Cadence", p.Soloist, p.SoloistScratch, p.CABundle))
	waitRecord(t, p.RuntimeDir, "airplay_config", fmt.Sprintf("enabled=false name=%q dir=%q scratch=%q", "Cadence", p.Airplay, p.AirplayScratch))
}

// rule 7 as in v1: while a ring sounds no media sender moves the level, and its slider is sent back
func TestNoSliderMovesTheLevelDuringARing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("audiod runs on linux only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tr := pickleTree(t)
	tr.inputDevice("event1", "mute")
	tr.write("data/cadence/state/volume", `{"level":25}`)
	p := pickle.PathsUnder(tr.root)
	if err := audiodtest.WriteFlags(p.RuntimeDir, "--soloist"); err != nil {
		t.Fatal(err)
	}
	b, logs := newBoard(t, tr, pickle.WithAudiod(exe), pickle.WithSwitch(positions(false)))
	r := b.Alarms()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx, alarms.Hooks{})
	}()
	t.Cleanup(func() { cancel(); <-done })
	if _, err := r.Book().SetTimer("tea", 1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !r.Ringing() {
		if time.Now().After(deadline) {
			t.Fatal("no ring")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// soloist's first level goes out once audiod is up, and the fake's slider answers it 5 above
	if _, ok := b.AudioIO(); !ok {
		t.Fatal("no audio")
	}
	waitLog(t, logs, "msg=media_volume_refused sender=soloist reason=ring")
	if s := state(t, find(t, b, "audio.output")); !strings.Contains(s, `"level":25,`) {
		t.Fatalf("output %s", s)
	}
}
