package audiod_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type rec struct {
	at    time.Time
	level slog.Level
	msg   string
	attrs map[string]any
}

// recorder is a slog handler that keeps every record for assertions
type recorder struct {
	mu   sync.Mutex
	recs []rec
}

func (*recorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *recorder) Handle(_ context.Context, sr slog.Record) error {
	attrs := map[string]any{}
	sr.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec{at: sr.Time, level: sr.Level, msg: sr.Message, attrs: attrs})
	return nil
}

func (r *recorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *recorder) WithGroup(string) slog.Handler      { return r }

func (r *recorder) all(msg string) []rec {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []rec
	for _, x := range r.recs {
		if x.msg == msg {
			out = append(out, x)
		}
	}
	return out
}

func (r *recorder) count(msg string) int { return len(r.all(msg)) }

func (r *recorder) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, x := range r.recs {
		fmt.Fprintf(&b, "%s %s %v\n", x.level, x.msg, x.attrs)
	}
	return b.String()
}

type harness struct {
	sup  *audiod.Supervisor
	dir  string // the socket's directory, where the fake records
	log  *recorder
	stop func()
}

func startSupervisor(t *testing.T, level int, tm audiod.Timings, args ...string) *harness {
	t.Helper()
	return startSupervisorWith(t, level, tm, nil, args...)
}

func startSupervisorWith(t *testing.T, level int, tm audiod.Timings, opts []audiod.SupervisorOption, args ...string) *harness {
	t.Helper()
	return startSupervisorConfig(t, level, tm, &audiodv1.Configure{}, opts, args...)
}

func startSupervisorConfig(t *testing.T, level int, tm audiod.Timings, cfg *audiodv1.Configure, opts []audiod.SupervisorOption, args ...string) *harness {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("audiod runs on linux only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "run")
	logs := &recorder{}
	// a race-built fake would sleep a second at every exit
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "GORACE=atexit_sleep_ms=0"}
	spec := audiod.Spec{Binary: exe, Args: args, Env: env, Socket: filepath.Join(dir, "audiod.sock"), Configure: cfg}
	sup := audiod.NewSupervisor(spec, level, slog.New(logs), append([]audiod.SupervisorOption{audiod.WithTimings(tm)}, opts...)...)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sup.Run(ctx)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Error("Run did not return")
			}
		})
	}
	t.Cleanup(stop)
	return &harness{sup: sup, dir: dir, log: logs, stop: stop}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) waitLog(t *testing.T, msg string, n int) {
	t.Helper()
	eventually(t, fmt.Sprintf("%d x %s", n, msg), func() bool { return h.log.count(msg) >= n })
}

func (h *harness) waitRecord(t *testing.T, run int, kind, detail string) {
	t.Helper()
	eventually(t, fmt.Sprintf("run %d %s %s", run, kind, detail), func() bool {
		for _, r := range audiodtest.Records(h.dir) {
			if r.Run == run && r.Kind == kind && r.Detail == detail {
				return true
			}
		}
		return false
	})
}

// firstAfterConfigure is the first envelope of a run after its Configure
func firstAfterConfigure(recs []audiodtest.Record, run int) audiodtest.Record {
	seen := false
	for _, r := range recs {
		if r.Run != run {
			continue
		}
		if seen {
			return r
		}
		seen = r.Kind == "configure"
	}
	return audiodtest.Record{}
}

func assertVolumeBeforeStreams(t *testing.T, recs []audiodtest.Record) {
	t.Helper()
	volume := map[int]bool{}
	for _, r := range recs {
		switch r.Kind {
		case "volume_set":
			volume[r.Run] = true
		case "stream_start":
			if !volume[r.Run] {
				t.Fatalf("run %d: stream_start %s before any volume_set", r.Run, r.Detail)
			}
		}
	}
}

func processGone(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	return p.Signal(syscall.Signal(0)) != nil
}

func TestSupervisorRestartsWithBackoff(t *testing.T) {
	const (
		minDelay = 80 * time.Millisecond
		reset    = 2 * time.Second
	)
	h := startSupervisor(t, 40, audiod.Timings{BackoffMin: minDelay, BackoffMax: 5 * time.Second, BackoffReset: reset},
		"--exit-after-ready", "--hold-run=4", "--hold-ms=2500")
	h.waitLog(t, "audiod_start", 5)
	starts, exits := h.log.all("audiod_start"), h.log.all("audiod_exit")
	for i, s := range starts {
		if s.attrs["attempt"] != int64(i+1) {
			t.Fatalf("start %d: attrs %v", i, s.attrs)
		}
	}
	for i, e := range exits[:4] {
		if e.attrs["code"] != int64(3) {
			t.Fatalf("exit attrs %v", e.attrs)
		}
		if ran, _ := e.attrs["ran_ms"].(int64); i < 3 && ran >= reset.Milliseconds() {
			t.Fatalf("run %d took %d ms, past the reset window: the spawn is too slow for this test", i+1, ran)
		}
	}
	delay := func(i int) time.Duration { return starts[i+1].at.Sub(exits[i].at) }
	d1, d2, d3, d4 := delay(0), delay(1), delay(2), delay(3)
	if d1 < minDelay || d2 < 2*minDelay || d3 < 4*minDelay || d1 >= d2 || d2 >= d3 {
		t.Fatalf("delays %v %v %v do not double", d1, d2, d3)
	}
	if ran, _ := exits[3].attrs["ran_ms"].(int64); ran < reset.Milliseconds() {
		t.Fatalf("held run ran %d ms", ran)
	}
	if d4 < minDelay || d4 >= d3 {
		t.Fatalf("delay after a long run %v, want reset (before it %v)", d4, d3)
	}
}

func TestSafeModeAfterThreeCrashes(t *testing.T) {
	t.Run("inside the window", func(t *testing.T) {
		h := startSupervisor(t, 40, audiod.Timings{BackoffMin: 20 * time.Millisecond, BackoffMax: 20 * time.Millisecond, CrashWindow: 10 * time.Second},
			"--exit-after-ready")
		h.waitLog(t, "audiod_start", 5)
		if !h.sup.SafeMode() {
			t.Fatalf("no safe mode after 4 crashes:\n%s", h.log.dump())
		}
		h.stop()
		if n := h.log.count("audiod_safe_mode"); n != 1 {
			t.Fatalf("audiod_safe_mode logged %d times", n)
		}
	})
	t.Run("spread wider than the window", func(t *testing.T) {
		h := startSupervisor(t, 40, audiod.Timings{BackoffMin: 300 * time.Millisecond, BackoffMax: 300 * time.Millisecond, CrashWindow: 500 * time.Millisecond},
			"--exit-after-ready")
		h.waitLog(t, "audiod_start", 5)
		h.stop()
		if h.sup.SafeMode() || h.log.count("audiod_safe_mode") != 0 {
			t.Fatalf("safe mode with crashes 300 ms apart in a 500 ms window:\n%s", h.log.dump())
		}
	})
}

func TestAudiodRestartMidStream(t *testing.T) {
	h := startSupervisor(t, 40, audiod.Timings{BackoffMin: 300 * time.Millisecond}, "--crash-mid-stream")
	h.waitLog(t, "audiod_ready", 1)
	aio := h.sup.IO(nil)
	rep, ok := aio.Mic.(audio.MicFailureReporter)
	if !ok {
		t.Fatal("supervised mic does not report failures")
	}
	mic, err := aio.Mic.Open(context.Background(), audio.Trigger{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := within(t, mic); !ok {
		t.Fatal("no first mic packet")
	}
	w, err := aio.Speaker.Begin(&cadencev2.StreamStart{StreamId: "s1", Channels: 1})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Write(0, []byte{1}) // the fake crashes on it

	for open := true; open; {
		_, open = within(t, mic)
	}
	if !rep.MicFailure(mic) {
		t.Fatal("a mic cut by a crash is not reported as failed")
	}
	h.sup.SetVolume(55)
	if err := w.Write(1, []byte{2}); err == nil {
		t.Fatal("write to a crashed audiod succeeded")
	}
	began := time.Now()
	w.Abort()
	if d := time.Since(began); d > time.Second {
		t.Fatalf("Abort waited %v", d)
	}

	// streams begun while audiod comes back must not reach it before its VolumeSet
	hammered := make(chan struct{})
	go func() {
		defer close(hammered)
		for i := 0; h.log.count("audiod_ready") < 2 && i < 2000; i++ {
			if hw, err := aio.Speaker.Begin(&cadencev2.StreamStart{StreamId: fmt.Sprintf("h-%d", i)}); err == nil {
				hw.Abort()
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	h.waitLog(t, "audiod_ready", 2)
	<-hammered

	w2, err := aio.Speaker.Begin(&cadencev2.StreamStart{StreamId: "s2", Channels: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.Write(0, []byte{3}); err != nil {
		t.Fatal(err)
	}
	if err := w2.End(); err != nil {
		t.Fatal(err)
	}
	h.waitRecord(t, 2, "stream_end", "s2")
	// well inside the 2 s grace after which the writer gives up on StreamDone
	deadline := time.Now().Add(time.Second)
	for audiod.StreamPending(h.sup, "s2") {
		if time.Now().After(deadline) {
			t.Fatal("no StreamDone for s2")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h.log.count("audiod_stream_timeout") != 0 {
		t.Fatal("a stream timed out waiting for StreamDone")
	}

	recs := audiodtest.Records(h.dir)
	if r := firstAfterConfigure(recs, 1); r.Kind != "volume_set" || r.Detail != "40" {
		t.Fatalf("run 1 after configure: %+v", r)
	}
	if r := firstAfterConfigure(recs, 2); r.Kind != "volume_set" || r.Detail != "55" {
		t.Fatalf("run 2 after configure: %+v, want the latest level", r)
	}
	assertVolumeBeforeStreams(t, recs)
	if n := h.log.count("audiod_ready"); n != 2 {
		t.Fatalf("audiod_ready %d times", n)
	}
}

func TestSupervisedIOUnavailableWhileDown(t *testing.T) {
	h := startSupervisor(t, 40, audiod.Timings{})
	aio := h.sup.IO(nil)
	h.waitLog(t, "audiod_ready", 1)
	h.stop()
	if _, err := aio.Mic.Open(context.Background(), audio.Trigger{}); !errors.Is(err, audiod.ErrUnavailable) {
		t.Fatalf("open after stop: %v", err)
	}
	if _, err := aio.Speaker.Begin(&cadencev2.StreamStart{StreamId: "x"}); !errors.Is(err, audiod.ErrUnavailable) {
		t.Fatalf("begin after stop: %v", err)
	}
	if err := h.sup.StartTone("alarm-1", audiodv1.ToneKind_TONE_KIND_ALARM); !errors.Is(err, audiod.ErrUnavailable) {
		t.Fatalf("tone start after stop: %v", err)
	}
	if err := h.sup.StopTone("alarm-1"); !errors.Is(err, audiod.ErrUnavailable) {
		t.Fatalf("tone stop after stop: %v", err)
	}
	if err := h.sup.SetTransientLevel(60); !errors.Is(err, audiod.ErrUnavailable) {
		t.Fatalf("transient level after stop: %v", err)
	}
}

func TestUnhealthyAudiodIsKilledAndRestarted(t *testing.T) {
	h := startSupervisor(t, 40, audiod.Timings{PingEvery: 50 * time.Millisecond, PongTimeout: 200 * time.Millisecond, BackoffMin: 50 * time.Millisecond},
		"--no-pong")
	h.waitLog(t, "audiod_start", 2)
	if h.log.count("audiod_unhealthy") < 1 {
		t.Fatalf("no audiod_unhealthy:\n%s", h.log.dump())
	}
	if code := h.log.all("audiod_exit")[0].attrs["code"]; code != int64(-1) {
		t.Fatalf("exit code %v, want -1 (killed)", code)
	}
}

// a hung audiod stops draining its socket: sends must not block the
// supervisor, its health check, or the callers, for good
func TestHungAudiodIsKilledAndRestarted(t *testing.T) {
	h := startSupervisor(t, 40, audiod.Timings{BackoffMin: 50 * time.Millisecond}, "--hang")
	h.waitLog(t, "audiod_ready", 1)
	h.waitRecord(t, 1, "volume_set", "40")
	w, err := h.sup.IO(nil).Speaker.Begin(&cadencev2.StreamStart{StreamId: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 1000)
	failed := make(chan struct{})
	go func() {
		defer close(failed)
		for i := 0; i < 5000; i++ {
			if w.Write(uint32(i), frame) != nil {
				return
			}
		}
	}()
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("writes to a hung audiod still block")
	}
	began := time.Now()
	h.sup.SetVolume(30)
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("SetVolume blocked %v", d)
	}
	h.waitLog(t, "audiod_start", 2)
	if code := h.log.all("audiod_exit")[0].attrs["code"]; code != int64(-1) {
		t.Fatalf("exit code %v, want -1 (killed)", code)
	}
}

func TestUnreadyAudiodIsKilledAndRestarted(t *testing.T) {
	for stage, arg := range map[string]string{"dial": "--no-listen", "ready": "--no-ready"} {
		t.Run(stage, func(t *testing.T) {
			h := startSupervisor(t, 40, audiod.Timings{DialTimeout: 200 * time.Millisecond, ReadyTimeout: 200 * time.Millisecond, BackoffMin: 50 * time.Millisecond}, arg)
			h.waitLog(t, "audiod_start", 2)
			unready := h.log.all("audiod_unready")
			if len(unready) == 0 || unready[0].attrs["stage"] != stage {
				t.Fatalf("unready %v", unready)
			}
			if code := h.log.all("audiod_exit")[0].attrs["code"]; code != int64(-1) {
				t.Fatalf("exit code %v, want -1 (killed)", code)
			}
			if h.log.count("audiod_ready") != 0 {
				t.Fatal("audiod_ready without a Ready")
			}
		})
	}
}

// drops held back by the rate limit go out within the interval, without a later drop
func TestLogDropsReportedWithoutAnotherDrop(t *testing.T) {
	h := startSupervisor(t, 40, audiod.Timings{PingEvery: 50 * time.Millisecond, LogDropEvery: 500 * time.Millisecond}, "--stderr")
	eventually(t, "4 drops reported while audiod runs", func() bool {
		total := int64(0)
		for _, d := range h.log.all("audiod_log_dropped") {
			n, _ := d.attrs["count"].(int64)
			total += n
		}
		return total == 4
	})
	if h.log.count("audiod_stopped") != 0 || h.log.count("audiod_exit") != 0 {
		t.Fatal("drops reported only as the run ended")
	}
}

func TestVolumeFailureLogged(t *testing.T) {
	h := startSupervisor(t, 40, audiod.Timings{}, "--volume-fail")
	h.waitLog(t, "audiod_volume_failed", 1)
	if a := h.log.all("audiod_volume_failed")[0].attrs; a["volume"] != int64(40) || a["reason"] != "control" {
		t.Fatalf("attrs %v", a)
	}
}

func TestStderrForwarded(t *testing.T) {
	h := startSupervisor(t, 40, audiod.Timings{}, "--stderr")
	h.waitLog(t, "fake_event", 1)
	h.waitLog(t, "fake_ipc_event", 1)
	h.stop()

	ev := h.log.all("fake_event")[0]
	if ev.level != slog.LevelWarn || ev.attrs["source"] != "audiod" || ev.attrs["count"] != int64(3) || ev.attrs["device"] != "card0" {
		t.Fatalf("fake_event %s %v", ev.level, ev.attrs)
	}
	vol := h.log.all("fake_volume")
	if len(vol) != 1 || vol[0].level != slog.LevelInfo || vol[0].attrs["audiod_level"] != int64(40) || vol[0].attrs["ratio"] != 0.5 {
		t.Fatalf("fake_volume %v", vol)
	}
	if ipc := h.log.all("fake_ipc_event")[0]; ipc.attrs["source"] != "audiod" || ipc.attrs["n"] != int64(1) {
		t.Fatalf("fake_ipc_event %v", ipc.attrs)
	}
	if out := h.log.dump(); strings.Contains(out, "SECRET") || strings.Contains(out, "fake_long") || strings.Contains(out, "fake_nested") {
		t.Fatalf("a dropped line reached the log:\n%s", out)
	}
	drops := h.log.all("audiod_log_dropped")
	total := int64(0)
	for _, d := range drops {
		n, _ := d.attrs["count"].(int64)
		total += n
	}
	// the first drop is reported at once, the rest within a minute only when the run ends
	if total != 4 || len(drops) != 2 || drops[0].attrs["count"] != int64(1) {
		t.Fatalf("drops %v", drops)
	}
}

func TestStopHonoursSIGTERM(t *testing.T) {
	h := startSupervisor(t, 40, audiod.Timings{StopGrace: 5 * time.Second})
	h.waitLog(t, "audiod_ready", 1)
	began := time.Now()
	h.stop()
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("stop took %v", d)
	}
	h.waitRecord(t, 1, "term", "")
	pids := audiodtest.PIDs(h.dir)
	if len(pids) != 1 || !processGone(pids[0]) {
		t.Fatalf("pids %v left running", pids)
	}
	if h.log.count("audiod_exit") != 0 {
		t.Fatal("a requested stop was logged as a crash")
	}
}

func TestStopKillsAfterGrace(t *testing.T) {
	const grace = 300 * time.Millisecond
	h := startSupervisor(t, 40, audiod.Timings{StopGrace: grace}, "--ignore-term")
	h.waitLog(t, "audiod_ready", 1)
	began := time.Now()
	h.stop()
	if d := time.Since(began); d < grace {
		t.Fatalf("stop took %v, before the grace", d)
	}
	pids := audiodtest.PIDs(h.dir)
	if len(pids) != 1 || !processGone(pids[0]) {
		t.Fatalf("pids %v left running", pids)
	}
}

// safe mode restarts audiod with the vendor engines off: the button alone begins sessions, and nothing ever says
// the wake word works
func TestSafeModeTurnsTheVendorEnginesOff(t *testing.T) {
	inLog := &recorder{}
	in := input.New(input.Options{Log: slog.New(inLog)})
	engines := &audiodv1.VendorEngines{Enabled: true, StockRoots: []string{"/stock"}, Library: "lib.so", ActivationModel: "model"}
	cfg := &audiodv1.Configure{Capture: &audiodv1.CaptureConfig{Backend: audiodv1.Backend_BACKEND_ALSA, Engines: engines}}
	tm := audiod.Timings{BackoffMin: 20 * time.Millisecond, BackoffMax: 20 * time.Millisecond, CrashWindow: 10 * time.Second}
	// without safe mode, the 4th run would answer that its engines hear the wake word
	h := startSupervisorConfig(t, 40, tm, cfg, []audiod.SupervisorOption{audiod.WithInput(in)}, "--crash-runs=3", "--vendor-ms=1")
	on := fmt.Sprintf("enabled=true roots=%q library=%q activation=%q command=%q preset=%q words=[]", []string{"/stock"}, "lib.so", "model", "", "")
	for run := 1; run <= 3; run++ {
		h.waitRecord(t, run, "configure_engines", on)
	}
	h.waitRecord(t, 4, "configure_engines", strings.Replace(on, "enabled=true", "enabled=false", 1))
	if !h.sup.SafeMode() || !engines.GetEnabled() {
		t.Fatal("no safe mode, or the spec's own Configure changed")
	}
	eventually(t, "the disabled verdict", func() bool { return inLog.count("wake_status") == 1 })
	if st := inLog.all("wake_status")[0].attrs; st["available"] != false || st["code"] != "disabled" {
		t.Fatalf("wake_status %v", st)
	}
	var last string
	for {
		select {
		case s := <-in.Capability().State():
			last = string(s)
			continue
		default:
		}
		break
	}
	if last != `{"muted":false,"wake":false}` {
		t.Fatalf("input state %s", last)
	}
}

// safe mode drops the keyword engine too: a model that crashes audiod leaves the button, never a crash loop
func TestSafeModeTurnsTheKeywordEngineOff(t *testing.T) {
	keyword := &audiodv1.KeywordEngine{Kind: "vk-kws", ModelPath: "/model.bin", ModelSha256: "ab", PosThreshold: 0.999, SpThreshold: 0.2, RearmFrames: 147, ConsecutiveCalls: 1}
	cfg := &audiodv1.Configure{Capture: &audiodv1.CaptureConfig{Backend: audiodv1.Backend_BACKEND_ALSA, Keyword: keyword}}
	tm := audiod.Timings{BackoffMin: 20 * time.Millisecond, BackoffMax: 20 * time.Millisecond, CrashWindow: 10 * time.Second}
	h := startSupervisorConfig(t, 40, tm, cfg, nil, "--crash-runs=3")
	on := fmt.Sprintf("kind=%q model=%q sha256=%q pos=0.999 sp=0.2 rearm=147 consecutive=1", "vk-kws", "/model.bin", "ab")
	for run := 1; run <= 3; run++ {
		h.waitRecord(t, run, "configure_keyword", on)
	}
	h.waitRecord(t, 4, "configure", "")
	for _, r := range audiodtest.Records(h.dir) {
		if r.Run == 4 && r.Kind == "configure_keyword" {
			t.Fatalf("safe mode sent the keyword engine: %s", r.Detail)
		}
	}
	if !h.sup.SafeMode() || cfg.GetCapture().GetKeyword() == nil {
		t.Fatal("no safe mode, or the spec's own Configure changed")
	}
}
