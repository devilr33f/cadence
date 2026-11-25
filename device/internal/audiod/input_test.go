package audiod_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// fakeInput sends the mute switch's position at every Ready and keeps the events audiod routes to it
type fakeInput struct {
	muted bool

	mu      sync.Mutex
	readies int
	gone    int
	events  []string
}

func (f *fakeInput) InputReady(send func(*audiodv1.Envelope) error) {
	f.mu.Lock()
	f.readies++
	f.mu.Unlock()
	_ = send(&audiodv1.Envelope{Body: &audiodv1.Envelope_MuteState{MuteState: &audiodv1.MuteState{Muted: f.muted}}})
}

func (f *fakeInput) InputEnvelope(env *audiodv1.Envelope) {
	var e string
	switch b := env.GetBody().(type) {
	case *audiodv1.Envelope_WakeEvent:
		e = "wake " + b.WakeEvent.GetEngine()
	case *audiodv1.Envelope_CommandWord:
		e = fmt.Sprintf("command %d", b.CommandWord.GetId())
	case *audiodv1.Envelope_VendorStatus:
		e = fmt.Sprintf("vendor %t %s", b.VendorStatus.GetEngines(), b.VendorStatus.GetCode())
	default:
		e = fmt.Sprintf("other %T", b)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeInput) InputGone() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gone++
}

func (f *fakeInput) seen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fmt.Sprintf("readies %d gone %d events %v", f.readies, f.gone, f.events)
}

// before returns whether, in every run, first comes before any second
func before(recs []audiodtest.Record, first, second string) error {
	seen := map[int]bool{}
	for _, r := range recs {
		switch r.Kind {
		case first:
			seen[r.Run] = true
		case second:
			if !seen[r.Run] {
				return fmt.Errorf("run %d: %s before any %s", r.Run, second, first)
			}
		}
	}
	return nil
}

// the mute switch reaches every audiod right after its level and duck
func TestMuteStateAfterEveryReady(t *testing.T) {
	in := &fakeInput{muted: true}
	h := startSupervisorWith(t, 40, audiod.Timings{BackoffMin: 300 * time.Millisecond}, []audiod.SupervisorOption{audiod.WithInput(in)}, "--crash-mid-stream")
	h.waitLog(t, "audiod_ready", 1)
	w, err := h.sup.IO(nil).Speaker.Begin(&cadencev2.StreamStart{StreamId: "s1", Channels: 1})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Write(0, []byte{1}) // the fake crashes on it
	h.waitLog(t, "audiod_ready", 2)
	h.waitRecord(t, 2, "mute_state", "true")

	recs := audiodtest.Records(h.dir)
	for run := 1; run <= 2; run++ {
		got := strings.Join(afterConfigure(recs, run), ", ")
		if !strings.HasPrefix(got, "volume_set 40, duck_set false, mute_state true") {
			t.Fatalf("run %d after configure: %s", run, got)
		}
	}
	eventually(t, "the first connection gone", func() bool { return in.seen() == "readies 2 gone 1 events []" })
}

func TestWakeCommandAndVendorReachTheInput(t *testing.T) {
	in := &fakeInput{}
	h := startSupervisorWith(t, 40, audiod.Timings{}, []audiod.SupervisorOption{audiod.WithInput(in)},
		"--vendor-ms=1", "--vendor-code=command_model_missing", "--wake")
	h.waitLog(t, "audiod_ready", 1)
	want := "readies 1 gone 0 events [vendor true command_model_missing wake stock command 2]"
	eventually(t, want, func() bool { return in.seen() == want })
}

func startPending(t *testing.T, backend audiodv1.Backend) *harness {
	t.Helper()
	cfg := &audiodv1.Configure{Capture: &audiodv1.CaptureConfig{Backend: backend}}
	h := startSupervisorConfig(t, 40, audiod.Timings{FirstPacket: 200 * time.Millisecond, FirstPacketVendor: 5 * time.Second}, cfg,
		[]audiod.SupervisorOption{audiod.WithInput(&fakeInput{})}, "--vendor-ms=800")
	h.waitLog(t, "audiod_ready", 1)
	return h
}

// while audiod probes the vendor engines, a session's first packet waits for their verdict: the long wait, not 2 s
func TestFirstPacketWaitsForTheVendorVerdict(t *testing.T) {
	t.Run("alsa, pending", func(t *testing.T) {
		h := startPending(t, audiodv1.Backend_BACKEND_ALSA)
		mic, err := h.sup.IO(nil).Mic.Open(context.Background(), audio.Trigger{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := within(t, mic); !ok {
			t.Fatalf("no first packet while the verdict was pending:\n%s", h.log.dump())
		}
		if h.log.count("audiod_mic_timeout") != 0 {
			t.Fatal("the mic timed out while the verdict was pending")
		}
	})
	t.Run("file, no verdict to wait for", func(t *testing.T) {
		h := startPending(t, audiodv1.Backend_BACKEND_FILE)
		aio := h.sup.IO(nil)
		mic, err := aio.Mic.Open(context.Background(), audio.Trigger{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := within(t, mic); ok {
			t.Fatal("a packet came before the short wait ran out")
		}
		if !aio.Mic.(audio.MicFailureReporter).MicFailure(mic) || h.log.count("audiod_mic_timeout") != 1 {
			t.Fatalf("no mic timeout:\n%s", h.log.dump())
		}
	})
}

// blockingInput holds its first InputReady until released
type blockingInput struct {
	*fakeInput
	entered, release chan struct{}
	once, freed      sync.Once
}

func (b *blockingInput) InputReady(send func(*audiodv1.Envelope) error) {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	b.fakeInput.InputReady(send)
}

func (b *blockingInput) free() { b.freed.Do(func() { close(b.release) }) }

// no capture starts on an audiod before the switch's position went out to it, however long that takes
func TestNoCaptureBeforeTheMuteState(t *testing.T) {
	in := &blockingInput{fakeInput: &fakeInput{muted: true}, entered: make(chan struct{}), release: make(chan struct{})}
	h := startSupervisorWith(t, 40, audiod.Timings{}, []audiod.SupervisorOption{audiod.WithInput(in)})
	// registered after the harness, so it runs first: a stuck InputReady would hold the stop
	t.Cleanup(in.free)
	select {
	case <-in.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("InputReady never ran")
	}
	opened := make(chan error, 1)
	go func() {
		_, err := h.sup.IO(nil).Mic.Open(context.Background(), audio.Trigger{})
		opened <- err
	}()
	select {
	case err := <-opened:
		t.Fatalf("a mic opened (%v) while the switch's position was on its way", err)
	case <-time.After(100 * time.Millisecond):
	}
	in.free()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the mic never opened")
	}
	h.waitRecord(t, 1, "capture_start", "-1")
	if err := before(audiodtest.Records(h.dir), "mute_state", "capture_start"); err != nil {
		t.Fatal(err)
	}
}

// once the verdict came, a session's first packet waits the short time again
func TestFirstPacketWaitShortAfterTheVerdict(t *testing.T) {
	in := &fakeInput{}
	cfg := &audiodv1.Configure{Capture: &audiodv1.CaptureConfig{Backend: audiodv1.Backend_BACKEND_ALSA}}
	h := startSupervisorConfig(t, 40, audiod.Timings{FirstPacket: 200 * time.Millisecond, FirstPacketVendor: 5 * time.Second}, cfg,
		[]audiod.SupervisorOption{audiod.WithInput(in)}, "--vendor-ms=1", "--no-mic")
	// the input hears the verdict after the supervisor took it
	eventually(t, "the verdict", func() bool { return strings.Contains(in.seen(), "vendor true") })
	aio := h.sup.IO(nil)
	began := time.Now()
	mic, err := aio.Mic.Open(context.Background(), audio.Trigger{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := within(t, mic); ok {
		t.Fatal("a packet from a mic that sends none")
	}
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("the mic gave up after %v, the pending wait", d)
	}
	if !aio.Mic.(audio.MicFailureReporter).MicFailure(mic) || h.log.count("audiod_mic_timeout") != 1 {
		t.Fatalf("no mic timeout:\n%s", h.log.dump())
	}
}
