package midi_test

import (
	"io"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"testing"

	midi "git.shino.boo/devilreef/cadence/device/boards/yandex-midi"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func withLogs() (midi.Option, *logs.Pipeline) {
	p := logs.New(nil, nil, discard())
	return midi.WithLogs(telemetry.LogsEnv{Sink: p, Local: discard()}), p
}

// the logs capability comes with a pipeline and reads the kernel log where the board keeps it: a tree without one says
// it could not
func TestLogsCapability(t *testing.T) {
	plain, _ := newBoard(t, midiTree(t))
	if slices.Contains(ids(plain), "logs") {
		t.Fatal("logs without a pipeline")
	}
	opt, _ := withLogs()
	b, log := newBoard(t, midiTree(t), opt)
	if got := ids(b); got[len(got)-1] != "logs" {
		t.Fatalf("capabilities = %v", got)
	}
	if s := state(t, find(t, b, "logs")); s != `{"dropped":0,"rejected":0,"ringBytes":0,"sources":[]}` {
		t.Fatalf("state = %s", s)
	}
	waitLog(t, log, "msg=kmsg_failed")
}

// audiod is told whether its children's lines ship
func TestAudiodGetsLogsSet(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("audiod runs on linux only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tr := midiTree(t)
	tr.inputDevice("event1", "gpio-keys")
	opt, _ := withLogs()
	b, _ := newBoard(t, tr, midi.WithAudiod(exe), midi.WithSwitch(positions(false)), opt)
	if _, ok := b.AudioIO(); !ok {
		t.Fatal("no audio")
	}
	waitRecord(t, midi.PathsUnder(tr.root).RuntimeDir, "logs_set", "true")
}
