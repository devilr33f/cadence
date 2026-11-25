package sim_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// newAudiodSim runs the sim with this test binary as its audiod, given flags.
func newAudiodSim(t *testing.T, flags ...string) (board.Board, string, *logBuf) {
	t.Helper()
	return newAudiodSimIn(t, t.TempDir(), flags...)
}

// newAudiodSimIn runs it on a data dir the test may have prepared.
func newAudiodSimIn(t *testing.T, dir string, flags ...string) (board.Board, string, *logBuf) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("audiod runs on linux only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := audiodtest.WriteFlags(filepath.Join(dir, "run"), flags...); err != nil {
		t.Fatal(err)
	}
	logs := &logBuf{}
	b := sim.New(func(string) string { return "1" }, dir, slog.New(slog.NewJSONHandler(logs, nil)), sim.WithAudiod(exe))
	t.Cleanup(func() { _ = b.(io.Closer).Close() })
	return b, dir, logs
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitRecord(t *testing.T, dir, kind, detail string) {
	t.Helper()
	waitFor(t, kind+" "+detail, func() bool {
		for _, r := range audiodtest.Records(filepath.Join(dir, "run")) {
			if r.Kind == kind && r.Detail == detail {
				return true
			}
		}
		return false
	})
}

func audiodIO(t *testing.T, b board.Board, logs *logBuf) audio.IO {
	t.Helper()
	aio, ok := b.AudioIO()
	if !ok {
		t.Fatal("no audio io")
	}
	waitFor(t, "audiod_ready", func() bool { return strings.Contains(logs.String(), "audiod_ready") })
	return aio
}

func TestAudiodModeMicWritesFixture(t *testing.T) {
	b, dir, logs := newAudiodSim(t)
	aio := audiodIO(t, b, logs)
	if _, ok := aio.Mic.(audio.MicFailureReporter); !ok {
		t.Fatal("mic does not report failures")
	}
	want, err := os.ReadFile(filepath.Join("fixtures", "what-time.wav"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := aio.Mic.Open(ctx, audio.Trigger{Fixture: "what-time"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("mic closed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no mic packet")
	}
	micPath := filepath.Join(dir, "sim", "mic.wav")
	if got, err := os.ReadFile(micPath); err != nil || string(got) != string(want) {
		t.Fatalf("mic.wav: %d bytes, err %v", len(got), err)
	}
	waitRecord(t, dir, "configure", micPath)
	// the file was whole when audiod got CaptureStart
	waitRecord(t, dir, "capture_start", strconv.Itoa(len(want)))

	if _, err := aio.Mic.Open(ctx, audio.Trigger{Fixture: "missing"}); err == nil {
		t.Fatal("unknown fixture opened")
	}
}

func TestAudiodModeSpeakerSummary(t *testing.T) {
	b, dir, logs := newAudiodSim(t)
	aio := audiodIO(t, b, logs)
	out := filepath.Join(dir, "sim", "out")

	w, err := aio.Speaker.Begin(&cadencev2.StreamStart{StreamId: "st-1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := w.Write(uint32(i), []byte{1, 2, byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "st-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sum map[string]any
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatal(err)
	}
	if sum["packets"] != float64(3) || sum["duration_ms"] != float64(60) || sum["purpose"] != "reply" || len(sum) != 3 {
		t.Fatalf("summary = %s", raw)
	}
	waitRecord(t, dir, "stream_end", "st-1")
	if _, err := os.Stat(filepath.Join(out, "st-1.opusframes")); !os.IsNotExist(err) {
		t.Fatalf("opusframes written in audiod mode: %v", err)
	}

	w2, err := aio.Speaker.Begin(&cadencev2.StreamStart{StreamId: "st-2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.Write(0, []byte{1}); err != nil {
		t.Fatal(err)
	}
	w2.Abort()
	waitRecord(t, dir, "stream_cancel", "st-2")
	if _, err := os.Stat(filepath.Join(out, "st-2.json")); !os.IsNotExist(err) {
		t.Fatalf("summary written for an aborted stream: %v", err)
	}

	if _, err := aio.Speaker.Begin(&cadencev2.StreamStart{StreamId: "../x"}); err == nil {
		t.Fatal("unsafe stream id accepted")
	}
	for _, r := range audiodtest.Records(filepath.Join(dir, "run")) {
		if r.Kind == "stream_start" && r.Detail == "../x" {
			t.Fatal("unsafe stream id reached audiod")
		}
	}
}

// audiod starts at the device's own level and gets every level set after it
func TestAudiodModeFollowsOutputLevel(t *testing.T) {
	dir := t.TempDir()
	storeLevel(t, dir, `{"level":25}`)
	b, _, logs := newAudiodSimIn(t, dir)
	out := b.Capabilities()[0]
	aio := audiodIO(t, b, logs)
	waitRecord(t, dir, "volume_set", "25")
	if _, err := out.Command(context.Background(), "set_level", json.RawMessage(`{"level":70}`)); err != nil {
		t.Fatal(err)
	}
	waitRecord(t, dir, "volume_set", "70")

	say(t, dir, "what-time")
	select {
	case tr := <-aio.Triggers:
		if tr.Fixture != "what-time" {
			t.Fatalf("trigger = %+v", tr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no trigger")
	}

	pids := audiodtest.PIDs(filepath.Join(dir, "run"))
	if err := b.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	if len(pids) != 1 {
		t.Fatalf("pids %v", pids)
	}
	if p, err := os.FindProcess(pids[0]); err == nil && p.Signal(syscall.Signal(0)) == nil {
		t.Fatal("audiod still runs after Close")
	}
}

// the sim rings through its audiod: core runs the ringer the board built, and the tone audiod answers sounding stops
// with the ring
func TestAudiodModeRingsAlarmsAndTimers(t *testing.T) {
	b, dir, logs := newAudiodSim(t)
	ab, ok := b.(board.Alarms)
	if !ok || ab.Alarms() == nil {
		t.Fatal("no ringer in audiod mode")
	}
	r := ab.Alarms()
	audiodIO(t, b, logs)
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
	waitRecord(t, dir, "tone_start", "ring-1 TONE_KIND_TIMER")
	if !r.Ringing() {
		t.Fatal("not ringing")
	}
	r.Stop(alarms.StopButton)
	waitRecord(t, dir, "tone_stop", "ring-1")

	if plain, ok := sim.New(func(string) string { return "1" }, t.TempDir(), nil).(board.Alarms); ok && plain.Alarms() != nil {
		t.Fatal("a ringer without audiod")
	}
}

// rule 7 as in v1: no media sender moves the level while a ring sounds; the slider is sent back to the level in force
func TestAudiodModeRefusesASliderDuringARing(t *testing.T) {
	dir := t.TempDir()
	storeLevel(t, dir, `{"level":25}`)
	b, _, logs := newAudiodSimIn(t, dir, "--soloist")
	r := b.(board.Alarms).Alarms()
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
	waitFor(t, "the ring", r.Ringing)

	// soloist's first level goes out once audiod is up, and the fake's slider answers it 5 above
	audiodIO(t, b, logs)
	waitFor(t, "the refusal", func() bool {
		return strings.Contains(logs.String(), `"msg":"media_volume_refused","sender":"soloist","reason":"ring"`)
	})
	waitFor(t, "the slider sent back", func() bool {
		n := 0
		for _, rec := range audiodtest.Records(filepath.Join(dir, "run")) {
			if rec.Kind == "sender_volume_set" && rec.Detail == "sender=SENDER_SOLOIST level=25" {
				n++
			}
		}
		return n >= 2
	})
	if out := b.Capabilities()[0]; !strings.Contains(next(t, out), `"level":25`) {
		t.Fatal("the level in force moved")
	}
	if strings.Contains(logs.String(), `"msg":"media_volume",`) {
		t.Fatalf("a slider moved the level during a ring: %s", logs)
	}
}
