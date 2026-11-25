package pickle_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	pickle "git.shino.boo/devilreef/cadence/device/boards/yandex-pickle"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

func ids(b board.Board) []string {
	var out []string
	for _, p := range b.Capabilities() {
		out = append(out, p.Descriptor().ID)
	}
	return out
}

func find(t *testing.T, b board.Board, id string) capability.Provider {
	t.Helper()
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID == id {
			return p
		}
	}
	t.Fatalf("no %s", id)
	return nil
}

func state(t *testing.T, p capability.Provider) string {
	t.Helper()
	select {
	case s := <-p.State():
		return string(s)
	case <-time.After(time.Second):
		t.Fatal("no state")
		return ""
	}
}

func TestBoardContract(t *testing.T) {
	b, _ := newBoard(t, pickleTree(t))
	var _ board.Board = b
	var _ board.Inputs = b
	var _ board.Lights = b
	var _ board.SessionTracker = b
	var _ board.MediaReporter = b
	var _ board.Alarms = b
	var _ board.Volume = b
	var _ board.MediaPlayback = b
	if b.Volume() == nil || b.MediaPlaying() {
		t.Fatalf("volume %v, playing %v", b.Volume(), b.MediaPlaying())
	}
	if b.ID() != "yandex-pickle" {
		t.Fatalf("id = %q", b.ID())
	}
	// display.lights is core's, added for any board with lights
	if got := ids(b); !slices.Equal(got, []string{"audio.output", "audio.input", "media.player", "media.url", "media.eq",
		"media.viz", "wake.custom", "media.soloist", "media.airplay", "media.group", "alarms"}) {
		t.Fatalf("capabilities = %v", got)
	}
	if b.Input() == nil || b.Sessions() == nil || b.MediaSessions() == nil || b.Alarms() == nil {
		t.Fatal("no input, sessions, media sessions or alarms")
	}
}

// every board is built at each start, the chosen one alone runs: building one touches nothing on the device
func TestNewTouchesNothing(t *testing.T) {
	tr := newTree(t)
	log, logs := newLog()
	b := pickle.New("/elsewhere", log, pickle.WithRoot(tr.root))
	if b.ID() != "yandex-pickle" || logs.String() != "" {
		t.Fatalf("logs = %q", logs.String())
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOutputIsTheHardwareVolume(t *testing.T) {
	tr := pickleTree(t)
	b, _ := newBoard(t, tr)
	out := find(t, b, "audio.output")
	// the boot wrapper applies 30 when no level was kept, so the daemon starts there too
	if s := state(t, out); s != `{"level":30,"channels":2,"maxBitrate":128000}` {
		t.Fatalf("state = %s", s)
	}
	if _, err := out.Command(context.Background(), "set_level", json.RawMessage(`{"level":20}`)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, out); s != `{"level":20,"channels":2,"maxBitrate":128000}` {
		t.Fatalf("state after set_level = %s", s)
	}
	if got := tr.read("data/cadence/state/volume"); got != `{"level":20}` {
		t.Fatalf("stored = %q", got)
	}
	// nothing in it is configurable
	if err := out.Apply(context.Background(), 1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := out.Apply(context.Background(), 2, json.RawMessage(`{"level":90}`)); err == nil {
		t.Fatal("a level in the config")
	}
}

func TestOutputStartsAtTheKeptLevel(t *testing.T) {
	tr := pickleTree(t)
	tr.write("data/cadence/state/volume", `{"level":45}`)
	b, _ := newBoard(t, tr)
	if s := state(t, find(t, b, "audio.output")); s != `{"level":45,"channels":2,"maxBitrate":128000}` {
		t.Fatalf("state = %s", s)
	}
}

// the board keeps its state where its boot wrapper reads it, and says so when the config names another dir
func TestDataDirMismatchLogged(t *testing.T) {
	tr := pickleTree(t)
	log, logs := newLog()
	b := pickle.New(tr.path("elsewhere"), log, pickle.WithRoot(tr.root), pickle.WithoutSoloistUpdates())
	t.Cleanup(func() { _ = b.Close() })
	if _, err := find(t, b, "audio.output").Command(context.Background(), "set_level", json.RawMessage(`{"level":20}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), "msg=data_dir_mismatch") != 1 {
		t.Fatalf("logs = %q", logs.String())
	}
	if got := tr.read("data/cadence/state/volume"); got != `{"level":20}` {
		t.Fatalf("stored = %q", got)
	}
	// the data root itself, spelled another way, is no mismatch
	sameLog, sameLogs := newLog()
	same := pickle.New(tr.path("data/cadence")+string(os.PathSeparator), sameLog, pickle.WithRoot(tr.root), pickle.WithoutSoloistUpdates())
	t.Cleanup(func() { _ = same.Close() })
	_ = same.Capabilities()
	if strings.Contains(sameLogs.String(), "data_dir_mismatch") {
		t.Fatalf("logs = %q", sameLogs.String())
	}
}

func TestTelemetryProfile(t *testing.T) {
	var b board.Board = pickle.New("", nil, pickle.WithRoot(t.TempDir()))
	tp, ok := b.(board.Telemetry)
	if !ok {
		t.Fatal("pickle has no telemetry profile")
	}
	got := tp.Telemetry()
	if !got.Kmsg || !slices.Equal(got.Mounts, []string{"/data", "/tmp"}) || got.TempSensor != "" || len(got.LogFiles) != 0 {
		t.Fatalf("profile = %+v", got)
	}
}
