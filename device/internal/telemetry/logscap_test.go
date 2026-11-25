package telemetry_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/sources"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/sources/kmsgtest"
	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

type fakeSink struct {
	mu     sync.Mutex
	recs   []logs.Record
	levels map[string]logs.Severity
	def    logs.Severity
	sets   int
	stats  logs.Stats
}

func (s *fakeSink) Emit(r logs.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
}

func (s *fakeSink) SetLevels(bySource map[string]logs.Severity, def logs.Severity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.levels, s.def = maps.Clone(bySource), def
	s.sets++
}

func (s *fakeSink) Stats() logs.Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	st.Sources = slices.Clone(st.Sources)
	return st
}

func (s *fakeSink) setStats(st logs.Stats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats = st
}

func (s *fakeSink) records() []logs.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.recs)
}

func (s *fakeSink) applied() (map[string]logs.Severity, logs.Severity, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.levels), s.def, s.sets
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newLogs(t *testing.T, profile board.TelemetryProfile) (*telemetry.Logs, *fakeSink) {
	t.Helper()
	sink := &fakeSink{}
	return telemetry.NewLogs(telemetry.LogsOptions{Sink: sink, Profile: profile, KmsgPath: "kmsg", Log: quiet()}), sink
}

func run(t *testing.T, l *telemetry.Logs) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { l.Run(ctx) })
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

func apply(t *testing.T, l *telemetry.Logs, cfg string) error {
	t.Helper()
	return l.Apply(context.Background(), 1, json.RawMessage(cfg))
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLogsDescriptor(t *testing.T) {
	l, _ := newLogs(t, board.TelemetryProfile{})
	d := l.Descriptor()
	if d.ID != "logs" || d.Version != 1 || d.ConfigSchema != telemetry.LogsConfigSchema || d.StateSchema != telemetry.LogsStateSchema ||
		len(d.Secrets) != 0 || len(d.Commands) != 0 {
		t.Fatalf("descriptor %+v", d)
	}
	if _, err := l.Command(context.Background(), "x", nil); !errors.Is(err, capability.ErrUnsupported) {
		t.Fatalf("command: %v", err)
	}
}

func TestLogsLevelsApplied(t *testing.T) {
	l, sink := newLogs(t, board.TelemetryProfile{})
	if err := apply(t, l, `{"sources":{"kmsg":"debug","soloist":"warn","audiod":"error","cadenced":"info"}}`); err != nil {
		t.Fatal(err)
	}
	levels, def, _ := sink.applied()
	want := map[string]logs.Severity{"kmsg": logs.Debug, "soloist": logs.Warn, "audiod": logs.Error, "cadenced": logs.Info}
	if !maps.Equal(levels, want) || def != logs.Info {
		t.Fatalf("levels %v default %v", levels, def)
	}
	// a config is whole: what it leaves out goes back to the default
	if err := apply(t, l, `{}`); err != nil {
		t.Fatal(err)
	}
	if levels, def, _ := sink.applied(); len(levels) != 0 || def != logs.Info {
		t.Fatalf("levels %v default %v", levels, def)
	}
}

func TestLogsConfigRefused(t *testing.T) {
	l, sink := newLogs(t, board.TelemetryProfile{})
	for _, cfg := range []string{
		``, `null`, `[]`, `{"sources":{"a":"trace"}}`, `{"sources":{"Bad":"info"}}`, `{"sources":{"a_b":"info"}}`,
		`{"sources":{"` + strings.Repeat("a", 33) + `":"info"}}`, `{"sources":null}`, `{"kmsg":"yes"}`, `{"kmsg":null}`,
		`{"thirdParty":1}`, `{"other":true}`, `{} {}`, `{"traces":null}`, `{"traces":"off"}`,
	} {
		var applyErr *capability.ApplyError
		if err := apply(t, l, cfg); !errors.As(err, &applyErr) || applyErr.Code != "invalid" {
			t.Fatalf("%q: err = %v", cfg, err)
		}
	}
	if _, _, sets := sink.applied(); sets != 0 {
		t.Fatalf("a refused config set the levels %d times", sets)
	}
}

// opens hands the logs a fresh fake kernel log at each open
type opens struct {
	mu    sync.Mutex
	files []*kmsgtest.File
}

func (o *opens) open() (sources.KmsgFile, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f := kmsgtest.New()
	o.files = append(o.files, f)
	return f, nil
}

func (o *opens) all() []*kmsgtest.File {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.files)
}

// open is the latest file, once the reader went to its end
func (o *opens) reading(n int) func() bool {
	return func() bool {
		fs := o.all()
		return len(fs) == n && fs[n-1].Seeks() == 1 && !fs[n-1].Closed()
	}
}

// the kernel log is read where the board has one, until the config turns it off, and again once it no longer does
func TestLogsKmsgToggled(t *testing.T) {
	l, sink := newLogs(t, board.TelemetryProfile{Kmsg: true})
	o := &opens{}
	telemetry.SetKmsgOpen(l, o.open)
	run(t, l)
	waitUntil(t, "the kernel log read", o.reading(1))
	o.all()[0].Add("4,1,10,-;thermal: hot")
	waitUntil(t, "the kernel's line", func() bool { return len(sink.records()) == 1 })
	if r := sink.records()[0]; r.Source != "kmsg" || r.Severity != logs.Warn || r.Body != "thermal: hot" {
		t.Fatalf("record %+v", r)
	}

	if err := apply(t, l, `{"kmsg":false}`); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the reader stopped", func() bool { return o.all()[0].Closed() })
	if err := apply(t, l, `{"kmsg":false,"thirdParty":true}`); err != nil {
		t.Fatal(err)
	}
	if err := apply(t, l, `{}`); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the kernel log read again", o.reading(2))
}

// a reader that failed starts again once a config turns the kernel log off and on, even when both configs come before
// the reader's loop looks
func TestLogsKmsgFailedReaderRestarted(t *testing.T) {
	for range 20 {
		var attempts atomic.Int32
		l, _ := newLogs(t, board.TelemetryProfile{Kmsg: true})
		telemetry.SetKmsgOpen(l, func() (sources.KmsgFile, error) {
			if attempts.Add(1) == 1 {
				return nil, os.ErrPermission
			}
			return kmsgtest.New(), nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Go(func() { l.Run(ctx) })
		waitUntil(t, "the failed open", func() bool { return attempts.Load() == 1 })
		// the loop has taken the failure in by then
		time.Sleep(20 * time.Millisecond)
		if err := apply(t, l, `{"kmsg":false}`); err != nil {
			t.Fatal(err)
		}
		if err := apply(t, l, `{}`); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "the reader started again", func() bool { return attempts.Load() == 2 })
		cancel()
		wg.Wait()
	}
}

// a board without a kernel log never reads one, whatever the config says
func TestLogsKmsgNeedsTheBoard(t *testing.T) {
	l, _ := newLogs(t, board.TelemetryProfile{})
	o := &opens{}
	telemetry.SetKmsgOpen(l, o.open)
	run(t, l)
	if err := apply(t, l, `{"kmsg":true}`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(o.all()); n != 0 {
		t.Fatalf("opened %d times", n)
	}
}

// sent records what goes to audiod
type sent struct {
	mu   sync.Mutex
	sets []bool
}

func (s *sent) send(env *audiodv1.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ls := env.GetLogsSet(); ls != nil {
		s.sets = append(s.sets, ls.GetThirdParty())
	}
	return nil
}

func (s *sent) all() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sets)
}

func childLine(source, text string) *audiodv1.LogLine {
	return &audiodv1.LogLine{Source: source, Level: audiodv1.LogLevel_LOG_LEVEL_INFO, Text: text}
}

func TestLogsThirdPartyLive(t *testing.T) {
	l, sink := newLogs(t, board.TelemetryProfile{})
	a := &sent{}
	l.LogsReady(a.send)
	if got := a.all(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("on connect: %v", got)
	}
	if err := apply(t, l, `{"thirdParty":false}`); err != nil {
		t.Fatal(err)
	}
	// unchanged: nothing more goes
	if err := apply(t, l, `{"thirdParty":false,"kmsg":false}`); err != nil {
		t.Fatal(err)
	}
	if got := a.all(); !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("after the change: %v", got)
	}
	// a child line audiod sent before it took the change is dropped here; its own lines still go
	l.LogLine(childLine("shairport-sync", "late"))
	l.LogLine(childLine("audiod", `{"level":"INFO","msg":"x"}`))
	if got := sink.records(); len(got) != 1 || got[0].Source != "audiod" {
		t.Fatalf("records %+v", got)
	}

	// a restarted audiod is told again
	l.LogsGone()
	if err := apply(t, l, `{"thirdParty":false,"sources":{"kmsg":"warn"}}`); err != nil {
		t.Fatal(err)
	}
	b := &sent{}
	l.LogsReady(b.send)
	if got := b.all(); !slices.Equal(got, []bool{false}) {
		t.Fatalf("after the reconnect: %v", got)
	}
	if err := apply(t, l, `{}`); err != nil {
		t.Fatal(err)
	}
	if got := b.all(); !slices.Equal(got, []bool{false, true}) {
		t.Fatalf("back on: %v", got)
	}
	if got := a.all(); len(got) != 2 {
		t.Fatalf("the gone connection got %v", got)
	}
}

func TestLogsLineRouting(t *testing.T) {
	l, sink := newLogs(t, board.TelemetryProfile{})
	for _, src := range []string{"audiod", "soloist", "shairport-sync", "nqptp", "dbus", "avahi", "other", ""} {
		l.LogLine(childLine(src, "line of "+src))
	}
	want := []string{"audiod", "soloist", "shairport-sync", "nqptp", "dbus", "avahi", "audiod-child", "audiod-child"}
	got := sink.records()
	if len(got) != len(want) {
		t.Fatalf("records %+v", got)
	}
	for i, r := range got {
		if r.Source != want[i] || r.Severity != logs.Info || r.Truncated {
			t.Fatalf("record %d = %+v", i, r)
		}
	}
	for _, c := range []struct {
		level audiodv1.LogLevel
		sev   logs.Severity
	}{
		{audiodv1.LogLevel_LOG_LEVEL_DEBUG, logs.Debug}, {audiodv1.LogLevel_LOG_LEVEL_INFO, logs.Info},
		{audiodv1.LogLevel_LOG_LEVEL_WARN, logs.Warn}, {audiodv1.LogLevel_LOG_LEVEL_ERROR, logs.Error},
		{audiodv1.LogLevel_LOG_LEVEL_UNSPECIFIED, logs.Info}, {audiodv1.LogLevel(42), logs.Info},
	} {
		l.LogLine(&audiodv1.LogLine{Source: "nqptp", Level: c.level, Text: "x"})
		recs := sink.records()
		if r := recs[len(recs)-1]; r.Severity != c.sev {
			t.Fatalf("level %v: severity %v", c.level, r.Severity)
		}
	}
	// audiod's own cut, and a line longer than audiod ever sends, cut here
	l.LogLine(&audiodv1.LogLine{Source: "soloist", Level: audiodv1.LogLevel_LOG_LEVEL_WARN, Text: "abc…", Truncated: true})
	l.LogLine(childLine("avahi", strings.Repeat("a", logs.MaxLine+1)))
	recs := sink.records()
	if r := recs[len(recs)-2]; !r.Truncated || r.Body != "abc…" {
		t.Fatalf("cut by audiod: %+v", r)
	}
	if r := recs[len(recs)-1]; !r.Truncated || len(r.Body) != logs.MaxLine {
		t.Fatalf("cut here: %d bytes, truncated %v", len(r.Body), r.Truncated)
	}
	// the rest of a line audiod sent partially before
	l.LogLine(&audiodv1.LogLine{Source: "nqptp", Level: audiodv1.LogLevel_LOG_LEVEL_INFO, Text: "rest", Continued: true})
	recs = sink.records()
	if r := recs[len(recs)-1]; !r.Continued || r.Truncated || r.Body != "rest" {
		t.Fatalf("continued: %+v", r)
	}
}

type states struct {
	mu  sync.Mutex
	got []string
	at  []time.Time
}

func (s *states) watch(t *testing.T, l *telemetry.Logs) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-l.State():
				s.mu.Lock()
				s.got = append(s.got, string(b))
				s.at = append(s.at, time.Now())
				s.mu.Unlock()
			}
		}
	})
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

func (s *states) all() ([]string, []time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got), slices.Clone(s.at)
}

// the state names every key, goes out at the start and on change, and at most once per interval
func TestLogsStateRateLimited(t *testing.T) {
	l, sink := newLogs(t, board.TelemetryProfile{})
	const every = 150 * time.Millisecond
	telemetry.SetStateEvery(l, every)
	st := &states{}
	st.watch(t, l)
	run(t, l)
	schema := compile(t, telemetry.LogsStateSchema)
	waitUntil(t, "the first state", func() bool { got, _ := st.all(); return len(got) == 1 })
	got, _ := st.all()
	if got[0] != `{"dropped":0,"rejected":0,"ringBytes":0,"sources":[]}` {
		t.Fatalf("first state %s", got[0])
	}
	check(t, schema, got[0], true)

	sink.setStats(logs.Stats{Dropped: 3, Rejected: 1, RingBytes: 900, Cut: 2, Sources: []string{"audiod", "kmsg"}})
	waitUntil(t, "the changed state", func() bool { got, _ := st.all(); return len(got) == 2 })
	sink.setStats(logs.Stats{Dropped: 4, RingBytes: 1000, Sources: []string{"audiod"}})
	waitUntil(t, "the next change", func() bool { got, _ := st.all(); return len(got) == 3 })
	time.Sleep(3 * every)
	got, at := st.all()
	if len(got) != 3 {
		t.Fatalf("states %q", got)
	}
	if got[1] != `{"dropped":3,"rejected":1,"ringBytes":900,"sources":["audiod","kmsg"]}` {
		t.Fatalf("state %s", got[1])
	}
	for i, s := range got {
		check(t, schema, s, true)
		if i > 0 && at[i].Sub(at[i-1]) < every-10*time.Millisecond {
			t.Fatalf("states %d and %d %v apart", i-1, i, at[i].Sub(at[i-1]))
		}
	}
}

// every file the board names is tailed under its source
func TestLogsTailBoardFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radio.log")
	if err := os.WriteFile(path, []byte("before the start\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, sink := newLogs(t, board.TelemetryProfile{LogFiles: []board.LogFile{{Source: "radio", Path: path}}})
	run(t, l)
	deadline := time.Now().Add(5 * time.Second)
	for len(sink.records()) == 0 && time.Now().Before(deadline) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test file
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("firmware up\n")
		_ = f.Close()
		time.Sleep(100 * time.Millisecond)
	}
	recs := sink.records()
	if len(recs) == 0 || recs[0].Source != "radio" || recs[0].Body != "firmware up" {
		t.Fatalf("records %+v", recs)
	}
}

// traces switches the device's spans; a config that leaves it out turns them back on
func TestLogsTracesSwitch(t *testing.T) {
	l, _ := newLogs(t, board.TelemetryProfile{})
	restore := tracing.Use(noop.NewTracerProvider())
	t.Cleanup(restore)
	if err := apply(t, l, `{"traces":false}`); err != nil || tracing.Enabled() {
		t.Fatalf("traces off: err %v, enabled %v", err, tracing.Enabled())
	}
	if err := apply(t, l, `{}`); err != nil || !tracing.Enabled() {
		t.Fatalf("traces default: err %v, enabled %v", err, tracing.Enabled())
	}
}
