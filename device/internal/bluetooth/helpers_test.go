package bluetooth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/btfake"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	phoneAddr = "AA:BB:CC:DD:EE:01"
	phoneName = "Pixel Маши"
)

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

type sessions map[string]cadencev2.Trigger

func (s sessions) Trigger(id string) (cadencev2.Trigger, bool) {
	t, ok := s[id]
	return t, ok
}

type senders struct {
	mu     sync.Mutex
	moves  []uint32
	follow func()
}

func (s *senders) SenderVolume(sender audiodv1.Sender, level uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sender == audiodv1.Sender_SENDER_BLUETOOTH {
		s.moves = append(s.moves, level)
	}
}

func (s *senders) FollowLevel(f func()) { s.follow = f }

func (s *senders) got() []uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.moves)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// audiod is a fake connection: the frames sent, and a dup of each fd beside its frame
type audiod struct {
	mu   sync.Mutex
	envs []*audiodv1.Envelope
	fds  []*os.File
}

func (a *audiod) send(env *audiodv1.Envelope) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.envs = append(a.envs, env)
	return nil
}

func (a *audiod) sendFD(env *audiodv1.Envelope, fd int) error {
	dup, err := syscall.Dup(fd)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.envs = append(a.envs, env)
	a.fds = append(a.fds, os.NewFile(uintptr(dup), "pcm"))
	return nil
}

func (a *audiod) all() []*audiodv1.Envelope {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.envs)
}

func (a *audiod) fd(i int) *os.File {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i >= len(a.fds) {
		return nil
	}
	return a.fds[i]
}

type harness struct {
	t       *testing.T
	be      *btfake.Backend
	b       *bluetooth.Bluetooth
	logs    *syncBuf
	clock   *clock
	senders *senders
	audiod  *audiod
	level   int
	levelMu sync.Mutex
	states  chan string
	// what bluetooth.output told the board: "route <address>", "lower <max>"
	outMu  sync.Mutex
	outLog []string
	// stop ends Run, as cadenced's exit does; the cleanup calls it too
	stop func()
}

func newHarness(t *testing.T, with ...func(h *harness, o *bluetooth.Options)) *harness {
	t.Helper()
	h := &harness{t: t, be: btfake.New(), logs: &syncBuf{}, clock: &clock{now: time.Unix(1_800_000_000, 0)},
		senders: &senders{}, audiod: &audiod{}, level: 30, states: make(chan string, 64)}
	opts := bluetooth.Options{
		Dial:     func(context.Context) (bluetooth.Backend, error) { return h.be, nil },
		Senders:  h.senders,
		Level:    h.currentLevel,
		Sessions: sessions{"wake-1": cadencev2.Trigger_TRIGGER_WAKE, "follow-1": cadencev2.Trigger_TRIGGER_FOLLOW_UP},
		Now:      h.clock.Now,
	}
	for _, f := range with {
		f(h, &opts)
	}
	h.b = bluetooth.New(opts, slog.New(slog.NewJSONHandler(h.logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.b.Run(ctx)
	}()
	var once sync.Once
	h.stop = func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(func() {
		h.stop()
		for _, f := range h.audiod.fds {
			_ = f.Close()
		}
		logs := []byte(h.logs.String())
		if bytes.Contains(logs, []byte(phoneName)) || bytes.Contains(logs, []byte(phoneAddr)) ||
			bytes.Contains(logs, []byte(speakerName)) || bytes.Contains(logs, []byte(speakerAddr)) {
			t.Errorf("a name or an address in the logs: %s", h.logs)
		}
	})
	return h
}

func (h *harness) currentLevel() (int, bool) {
	h.levelMu.Lock()
	defer h.levelMu.Unlock()
	return h.level, true
}

// setLevel is the policy changing the level: it tells its followers
func (h *harness) setLevel(level int) {
	h.levelMu.Lock()
	h.level = level
	h.levelMu.Unlock()
	h.senders.follow()
}

func (h *harness) ready() { h.b.BluetoothReady(h.audiod.send, h.audiod.sendFD) }

func (h *harness) command(name, args string) (string, error) {
	h.t.Helper()
	out, err := h.b.Capability().Command(context.Background(), name, json.RawMessage(args))
	return string(out), err
}

func (h *harness) state() map[string]any {
	h.t.Helper()
	var last json.RawMessage
	for {
		select {
		case v := <-h.b.Capability().State():
			last = v
			continue
		default:
		}
		break
	}
	if last == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(last, &m); err != nil {
		h.t.Fatal(err)
	}
	return m
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

func (h *harness) waitCall(call string) {
	h.t.Helper()
	waitFor(h.t, call, func() bool { return slices.Contains(h.be.Calls(), call) })
}

func (h *harness) waitLog(s string) {
	h.t.Helper()
	waitFor(h.t, s, func() bool { return bytes.Contains([]byte(h.logs.String()), []byte(s)) })
}

func (h *harness) phoneID() string { return bluetooth.ShortID("00:11:22:33:44:55", phoneAddr) }

const (
	speakerAddr = "AA:BB:CC:DD:EE:10"
	speakerName = "JBL Flip Кухня"
)

// withOutput turns bluetooth.output on, its routes and lowers recorded, its scans short
func withOutput(known string) func(h *harness, o *bluetooth.Options) {
	return func(h *harness, o *bluetooth.Options) {
		o.Output = &bluetooth.OutputOptions{
			Route:     func(address string) { h.outEvent("route " + address) },
			Lower:     func(top int) { h.outEvent(fmt.Sprintf("lower %d", top)) },
			KnownFile: known,
			ScanFor:   20 * time.Millisecond,
		}
	}
}

func (h *harness) outEvent(e string) {
	h.outMu.Lock()
	defer h.outMu.Unlock()
	h.outLog = append(h.outLog, e)
}

func (h *harness) outEvents() []string {
	h.outMu.Lock()
	defer h.outMu.Unlock()
	return slices.Clone(h.outLog)
}

// waitRoute waits until the last route the board heard is address
func (h *harness) waitRoute(address string) {
	h.t.Helper()
	waitFor(h.t, "route "+address, func() bool {
		evs := h.outEvents()
		for i := len(evs) - 1; i >= 0; i-- {
			if strings.HasPrefix(evs[i], "route ") {
				return evs[i] == "route "+address
			}
		}
		return false
	})
}

func (h *harness) output(name, args string) (string, error) {
	h.t.Helper()
	out, err := h.b.OutputCapability().Command(context.Background(), name, json.RawMessage(args))
	return string(out), err
}

func (h *harness) outputState() map[string]any {
	h.t.Helper()
	var last json.RawMessage
	for {
		select {
		case v := <-h.b.OutputCapability().State():
			last = v
			continue
		default:
		}
		break
	}
	if last == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(last, &m); err != nil {
		h.t.Fatal(err)
	}
	return m
}

func (h *harness) idOf(address string) string { return bluetooth.ShortID("00:11:22:33:44:55", address) }
