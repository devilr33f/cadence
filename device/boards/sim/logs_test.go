package sim_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func logsOf(b board.Board) capability.Provider {
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID == "logs" {
			return p
		}
	}
	return nil
}

// the logs capability comes with a pipeline, and reports from the first look at the capabilities
func TestLogsCapability(t *testing.T) {
	plain := sim.New(func(string) string { return "1" }, t.TempDir(), nil)
	t.Cleanup(func() { _ = plain.(io.Closer).Close() })
	if logsOf(plain) != nil {
		t.Fatal("logs without a pipeline")
	}
	p := logs.New(nil, nil, discard())
	b := sim.New(func(string) string { return "1" }, t.TempDir(), nil, sim.WithLogs(telemetry.LogsEnv{Sink: p, Local: discard()}))
	t.Cleanup(func() { _ = b.(io.Closer).Close() })
	c := logsOf(b)
	if c == nil {
		t.Fatal("no logs capability")
	}
	select {
	case s := <-c.State():
		if string(s) != `{"dropped":0,"rejected":0,"ringBytes":0,"sources":[]}` {
			t.Fatalf("state %s", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no state")
	}
}

// audiod is told whether its children's lines ship, and its lines and its children's reach the pipeline; the local
// log gets audiod's own alone, the sim's log none of them
func TestAudiodModeShipsLogs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("audiod runs on linux only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := audiodtest.WriteFlags(filepath.Join(dir, "run"), "--stderr"); err != nil {
		t.Fatal(err)
	}
	simLog, local := &logBuf{}, &logBuf{}
	p := logs.New(nil, nil, discard())
	b := sim.New(func(string) string { return "1" }, dir, slog.New(slog.NewJSONHandler(simLog, nil)), sim.WithAudiod(exe),
		sim.WithLogs(telemetry.LogsEnv{Sink: p, Local: slog.New(slog.NewJSONHandler(local, nil))}))
	t.Cleanup(func() { _ = b.(io.Closer).Close() })
	_ = b.Capabilities()
	if _, ok := b.AudioIO(); !ok {
		t.Fatal("no audio")
	}
	waitFor(t, "audiod's and its children's lines", func() bool {
		src := p.Stats().Sources
		return slices.Contains(src, "audiod") && slices.Contains(src, "soloist") && slices.Contains(src, "audiod-child")
	})
	waitFor(t, "the LogsSet", func() bool {
		for _, r := range audiodtest.Records(filepath.Join(dir, "run")) {
			if r.Kind == "logs_set" && r.Detail == "true" {
				return true
			}
		}
		return false
	})
	if !strings.Contains(local.String(), "fake_event") || strings.Contains(simLog.String(), "fake_event") {
		t.Fatalf("local:\n%s\nsim:\n%s", local.String(), simLog.String())
	}
	if strings.Contains(local.String(), "SECRET") || strings.Contains(simLog.String(), "SECRET") {
		t.Fatal("a line the local log never keeps reached it")
	}
}
