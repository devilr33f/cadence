package soloist_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// a key that looks like none a real account has
const testKey = "TESTKEY-0123456789abcdef"

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeAudiod is the far end of SoloistReady's send: it keeps what it got, and
// answers probes through answer when set.
type fakeAudiod struct {
	mu     sync.Mutex
	sent   []*audiodv1.Envelope
	down   bool
	block  chan struct{} // a SenderVolumeSet waits for it to close
	answer func(*audiodv1.SoloistProbe) *audiodv1.SoloistProbeResult
	s      *soloist.Soloist
}

func (a *fakeAudiod) send(env *audiodv1.Envelope) error {
	a.mu.Lock()
	down, block, answer := a.down, a.block, a.answer
	if !down {
		a.sent = append(a.sent, proto.Clone(env).(*audiodv1.Envelope))
	}
	a.mu.Unlock()
	if down {
		return errors.New("closed")
	}
	if env.GetSenderVolumeSet() != nil && block != nil {
		<-block
	}
	if p := env.GetSoloistProbe(); p != nil && answer != nil {
		if r := answer(p); r != nil {
			go a.s.SoloistEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_SoloistProbeResult{SoloistProbeResult: r}})
		}
	}
	return nil
}

func (a *fakeAudiod) all() []*audiodv1.Envelope {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*audiodv1.Envelope(nil), a.sent...)
}

func (a *fakeAudiod) configs() []*audiodv1.SoloistConfig {
	var out []*audiodv1.SoloistConfig
	for _, e := range a.all() {
		if c := e.GetSoloistConfig(); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// levels are the SenderVolumeSet levels for soloist, in order
func (a *fakeAudiod) levels() []uint32 { return a.levelsFor(audiodv1.Sender_SENDER_SOLOIST) }

func (a *fakeAudiod) levelsFor(sender audiodv1.Sender) []uint32 {
	var out []uint32
	for _, e := range a.all() {
		if v := e.GetSenderVolumeSet(); v != nil && v.GetSender() == sender {
			out = append(out, v.GetLevel())
		}
	}
	return out
}

// kinds names each envelope sent, in order
func (a *fakeAudiod) kinds() []string {
	var out []string
	for _, e := range a.all() {
		out = append(out, string(e.ProtoReflect().WhichOneof(e.ProtoReflect().Descriptor().Oneofs().ByName("body")).Name()))
	}
	return out
}

type harness struct {
	t     *testing.T
	s     *soloist.Soloist
	vol   *volume.Setter
	store *volume.Store
	logs  *syncBuf
	opts  soloist.Options
	a     *fakeAudiod
	cap   capability.Provider
	// every state the capability published, in order
	stateMu sync.Mutex
	states  []string
}

type option func(*harness, *soloist.Options)

// withLevel stores level first: the setter starts with a level of its own
func withLevel(level int) option {
	return func(h *harness, _ *soloist.Options) {
		if err := h.store.Save(level); err != nil {
			h.t.Fatal(err)
		}
	}
}

func withRinging(ringing func() bool) option {
	return func(_ *harness, o *soloist.Options) { o.Ringing = ringing }
}

func newHarness(t *testing.T, opts ...option) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{t: t, store: volume.NewStore(dir), logs: &syncBuf{}, a: &fakeAudiod{}}
	h.opts = soloist.Options{
		Dir: filepath.Join(dir, "soloist"), ScratchDir: filepath.Join(dir, "run", "soloist"), CAFile: "/etc/ssl/certs/ca.pem",
	}
	for _, o := range opts {
		o(h, &h.opts)
	}
	log := slog.New(slog.NewJSONHandler(h.logs, nil))
	h.vol = volume.NewSetter(h.store, 50, log)
	h.s = soloist.New(h.vol, h.opts, log)
	h.a.s = h.s
	h.cap = h.s.Capability()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.s.Run(ctx)
	}()
	stateDone := make(chan struct{})
	go func() {
		defer close(stateDone)
		for {
			select {
			case <-ctx.Done():
				return
			case v := <-h.cap.State():
				h.stateMu.Lock()
				h.states = append(h.states, string(v))
				h.stateMu.Unlock()
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
		<-stateDone
	})
	return h
}

func (h *harness) ready() { h.s.SoloistReady(h.a.send) }

func (h *harness) apply(cfg string, secrets map[string]string) error {
	h.t.Helper()
	sa, ok := h.cap.(capability.SecretApplier)
	if !ok {
		h.t.Fatal("media.soloist takes no secrets")
	}
	return sa.ApplySecrets(context.Background(), 1, json.RawMessage(cfg), secrets)
}

func (h *harness) mustApply(cfg string, secrets map[string]string) {
	h.t.Helper()
	if err := h.apply(cfg, secrets); err != nil {
		h.t.Fatalf("apply %s: %v", cfg, err)
	}
}

func (h *harness) status(st *audiodv1.SoloistStatus) {
	h.s.SoloistEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_SoloistStatus{SoloistStatus: st}})
}

func (h *harness) senderVolume(level uint32) {
	h.senderVolumeFrom(audiodv1.Sender_SENDER_SOLOIST, level)
}

func (h *harness) senderVolumeFrom(sender audiodv1.Sender, level uint32) {
	h.s.SoloistEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_SenderVolume{SenderVolume: &audiodv1.SenderVolume{
		Sender: sender, Level: level,
	}}})
}

func (h *harness) allStates() []string {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	return append([]string(nil), h.states...)
}

// waitState waits until the latest state published is want
func (h *harness) waitState(want string) {
	h.t.Helper()
	waitFor(h.t, "state "+want, func() bool {
		s := h.allStates()
		return len(s) > 0 && s[len(s)-1] == want
	})
}

func (h *harness) waitLog(fragment string) {
	h.t.Helper()
	waitFor(h.t, fragment, func() bool { return strings.Contains(h.logs.String(), fragment) })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settle gives the soloist's goroutines time to act on what they will not
func settle() { time.Sleep(150 * time.Millisecond) }
