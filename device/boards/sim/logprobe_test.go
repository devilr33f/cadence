package sim_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

// probeSink keeps what the sim emits into its pipeline
type probeSink struct {
	mu   sync.Mutex
	recs []logs.Record
}

func (s *probeSink) Emit(r logs.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
}

func (*probeSink) SetLevels(map[string]logs.Severity, logs.Severity) {}

func (*probeSink) Stats() logs.Stats { return logs.Stats{Sources: []string{}} }

// probes are the records of source sim-probe
func (s *probeSink) probes() []logs.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []logs.Record
	for _, r := range s.recs {
		if r.Source == "sim-probe" {
			out = append(out, r)
		}
	}
	return out
}

type probeRig struct {
	dir        string
	sink       *probeSink
	log, local *logBuf
}

// newProbeRig is a sim with a pipeline (none when piped is false), its telemetry started
func newProbeRig(t *testing.T, piped bool) *probeRig {
	t.Helper()
	r := &probeRig{dir: t.TempDir(), sink: &probeSink{}, log: &logBuf{}, local: &logBuf{}}
	var opts []sim.Option
	if piped {
		opts = append(opts, sim.WithLogs(telemetry.LogsEnv{Sink: r.sink, Local: slog.New(slog.NewJSONHandler(r.local, nil))}))
	}
	b := sim.New(func(string) string { return "1" }, r.dir, slog.New(slog.NewJSONHandler(r.log, nil)), opts...)
	t.Cleanup(func() { _ = b.(io.Closer).Close() })
	_ = b.Capabilities()
	return r
}

func (r *probeRig) path() string { return filepath.Join(r.dir, "logprobe") }

// write puts text in the probe file whole, as the e2e's copy does
func (r *probeRig) write(t *testing.T, text string) {
	t.Helper()
	tmp := filepath.Join(r.dir, "logprobe.tmp")
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, r.path()); err != nil {
		t.Fatal(err)
	}
}

func (r *probeRig) gone() bool {
	_, err := os.Stat(r.path())
	return os.IsNotExist(err)
}

// each line of the probe file ships through the pipeline as sim-probe at info, cut where a source cuts a line, and
// never reaches a local log; the file goes
func TestLogProbeShipsItsLines(t *testing.T) {
	r := newProbeRig(t, true)
	long := strings.Repeat("x", 5000)
	r.write(t, "cadence probe one\r\n\ncadence probe «two»\n"+long+"\n")
	waitFor(t, "the probe's lines", func() bool { return len(r.sink.probes()) == 3 })
	got := r.sink.probes()
	for i, want := range []struct {
		body string
		cut  bool
	}{{"cadence probe one", false}, {"cadence probe «two»", false}, {long[:logs.MaxLine], true}} {
		if g := got[i]; g.Body != want.body || g.Truncated != want.cut || g.Severity != logs.Info || g.Continued {
			t.Fatalf("record %d: %q truncated %v severity %d", i, g.Body, g.Truncated, g.Severity)
		}
	}
	if !r.gone() {
		t.Fatal("the probe file stayed")
	}
	for _, l := range []*logBuf{r.log, r.local} {
		if strings.Contains(l.String(), "cadence probe") {
			t.Fatalf("a probe line reached a local log:\n%s", l.String())
		}
	}
}

// a last line without its newline may be half written: the file waits until it is whole
func TestLogProbeWaitsForAWholeLine(t *testing.T) {
	r := newProbeRig(t, true)
	r.write(t, "cadence probe half")
	time.Sleep(700 * time.Millisecond)
	if len(r.sink.probes()) != 0 || r.gone() {
		t.Fatal("a half written probe file was read")
	}
	r.write(t, "cadence probe half\n")
	waitFor(t, "the whole line", func() bool { return len(r.sink.probes()) == 1 })
	if b := r.sink.probes()[0].Body; b != "cadence probe half" {
		t.Fatalf("body %q", b)
	}
}

// a file past 64 KiB is dropped unread, said in the sim's log; without a pipeline the file is never read
func TestLogProbeBounds(t *testing.T) {
	r := newProbeRig(t, true)
	r.write(t, strings.Repeat("cadence probe huge\n", 64<<10/19+1))
	waitFor(t, "the huge file dropped", r.gone)
	time.Sleep(300 * time.Millisecond)
	if n := len(r.sink.probes()); n != 0 {
		t.Fatalf("%d lines of a huge file shipped", n)
	}
	if !strings.Contains(r.log.String(), `"msg":"sim_logprobe_too_large"`) || strings.Contains(r.log.String(), "cadence probe") {
		t.Fatalf("sim log:\n%s", r.log.String())
	}

	plain := newProbeRig(t, false)
	plain.write(t, "cadence probe unread\n")
	time.Sleep(700 * time.Millisecond)
	if plain.gone() || len(plain.sink.probes()) != 0 {
		t.Fatal("a sim without a pipeline read the probe file")
	}
}
