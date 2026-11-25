package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// a trigger with no link up is refused at once: it never begins a session once a link comes
func TestActivationWithoutLinkRefused(t *testing.T) {
	triggers := make(chan audio.Trigger, 4)
	b := fakeBoard{id: "fake", aio: &audio.IO{Triggers: triggers, Mic: chanMic{}}}
	ls, buf := startSync(t, b, openStore(t))
	p := ls.accept(t)
	// the device waits for the welcome: no session controller runs yet
	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	waitLog(t, buf, "activation_refused")
	if !strings.Contains(buf.String(), `"msg":"activation_refused","reason":"not_connected","trigger":"TRIGGER_WAKE"`) {
		t.Fatalf("log: %s", buf.String())
	}
	// the switch with no session ends nothing and is no activation
	triggers <- audio.Trigger{Mode: audio.Mute}
	p.handshake(false)
	waitLog(t, buf, "link_up")
	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	if s := p.recv().GetSessionStart(); s.GetTrigger() != cadencev2.Trigger_TRIGGER_BUTTON {
		t.Fatalf("first start = %v", s)
	}
	if n := strings.Count(buf.String(), `"msg":"activation_refused"`); n != 1 {
		t.Fatalf("%d refusals: %s", n, buf.String())
	}
}

type fakeButtons chan input.ButtonEvent

func (b fakeButtons) Events() <-chan input.ButtonEvent { return b }

// sink is a board's lights: it keeps the last ring it was given
type sink struct {
	mu   sync.Mutex
	ring []leds.RGB
}

func (s *sink) Ring(px []leds.RGB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring = append(s.ring[:0], px...)
	return nil
}

func (*sink) Button([]leds.RGB) error               { return nil }
func (*sink) Segments(map[leds.Segment]uint8) error { return nil }
func (*sink) Geometry() leds.Geometry               { return leds.Geometry{Ring: 4} }

func (s *sink) lit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, px := range s.ring {
		if px != (leds.RGB{}) {
			return true
		}
	}
	return false
}

// inputBoard has buttons, a mute switch and lights, as a speaker does; its audio has triggers of its own too, as the
// sim's say file. core takes the input's triggers itself
type inputBoard struct {
	fakeBoard
	in   *input.Input
	sink *sink
	say  chan audio.Trigger
}

func (b inputBoard) Input() *input.Input       { return b.in }
func (b inputBoard) Lights() (leds.Sink, bool) { return b.sink, true }
func (b inputBoard) AudioIO() (audio.IO, bool) {
	return audio.IO{Triggers: b.say, Mic: chanMic{}}, true
}
func (b inputBoard) Capabilities() []capability.Provider {
	return []capability.Provider{b.in.Capability()}
}

var (
	_ board.Inputs = inputBoard{}
	_ board.Lights = inputBoard{}
)

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// the buttons and the mute switch drive sessions over the link, and the lights show what the device does
func TestButtonsAndLightsOverTheLink(t *testing.T) {
	buttons := make(fakeButtons)
	b := inputBoard{fakeBoard: fakeBoard{id: "speaker"}, in: input.New(input.Options{Buttons: buttons}), sink: &sink{}, say: make(chan audio.Trigger, 1)}
	ls := newLinkServer(t)
	dir := t.TempDir()
	cfg := config.Config{Board: "speaker", ServerURL: ls.url, DeviceID: "dev-1", DeviceToken: token, DataDir: dir}
	buf := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- core.Run(ctx, cfg, []board.Board{b}, newLog(buf), "v1") }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	p := ls.accept(t)
	p.handshake(true)
	ids := map[string]bool{}
	for len(ids) < 2 {
		env := p.recv()
		if r := env.GetCapabilityReport(); r != nil {
			for _, d := range r.GetDescriptors() {
				ids[d.GetId()] = true
			}
		}
	}
	if !ids["audio.input"] || !ids[leds.CapabilityID] {
		t.Fatalf("reported %v", ids)
	}
	waitLog(t, buf, "link_up")
	if b.sink.lit() {
		t.Fatal("the ring is lit at rest")
	}

	press := func() {
		buttons <- input.ButtonEvent{Kind: input.Action, Pressed: true}
		buttons <- input.ButtonEvent{Kind: input.Action}
	}
	press()
	var start *cadencev2.SessionStart
	for start == nil {
		start = p.recv().GetSessionStart()
	}
	if start.GetTrigger() != cadencev2.Trigger_TRIGGER_BUTTON {
		t.Fatalf("start = %v", start)
	}
	eventually(t, "the ring listening", b.sink.lit)
	press()
	var cancelled *cadencev2.SessionCancel
	for cancelled == nil {
		cancelled = p.recv().GetSessionCancel()
	}
	if cancelled.GetSessionId() != start.GetSessionId() || cancelled.GetReason() != "local_cancel" {
		t.Fatalf("cancel = %v", cancelled)
	}
	eventually(t, "the ring idle", func() bool { return !b.sink.lit() })

	// the audio's own triggers come in beside the input's
	b.say <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	for start = nil; start == nil; {
		start = p.recv().GetSessionStart()
	}
	if start.GetTrigger() != cadencev2.Trigger_TRIGGER_WAKE {
		t.Fatalf("start = %v", start)
	}
	press()
	for cancelled = nil; cancelled == nil; {
		cancelled = p.recv().GetSessionCancel()
	}
	eventually(t, "the ring idle after the say session", func() bool { return !b.sink.lit() })

	// muted at rest the ring glows dim; a session that runs ends with the switch
	buttons <- input.ButtonEvent{Kind: input.MuteOn}
	eventually(t, "the ring muted", b.sink.lit)
	buttons <- input.ButtonEvent{Kind: input.MuteOff}
	eventually(t, "the ring idle after the unmute", func() bool { return !b.sink.lit() })
	press()
	for start = nil; start == nil; {
		start = p.recv().GetSessionStart()
	}
	buttons <- input.ButtonEvent{Kind: input.MuteOn}
	for cancelled = nil; cancelled == nil; {
		cancelled = p.recv().GetSessionCancel()
	}
	if cancelled.GetSessionId() != start.GetSessionId() || cancelled.GetReason() != "muted" {
		t.Fatalf("cancel = %v", cancelled)
	}

	// the owner's switches are kept in the data dir
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigSet{ConfigSet: &cadencev2.ConfigSet{Capability: leds.CapabilityID, Generation: 1, Json: `{"clock":false}`}}})
	eventually(t, "the switches kept", func() bool {
		_, err := os.Stat(filepath.Join(dir, "state", "lights"))
		return err == nil
	})
}

// errorSpeaker records the error sounds it is asked for
type errorSpeaker struct {
	mu  sync.Mutex
	ids []string
}

func (s *errorSpeaker) Begin(*cadencev2.StreamStart) (audio.StreamWriter, error) {
	return nil, os.ErrClosed
}

func (s *errorSpeaker) ErrorSound(id, lang string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, id+"/"+lang)
}

func (s *errorSpeaker) played() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ids...)
}

// a press with no link says so once, not at every press of a burst, and test audio says nothing
func TestActivationWithoutLinkSaysOffline(t *testing.T) {
	triggers := make(chan audio.Trigger, 4)
	sp := &errorSpeaker{}
	b := fakeBoard{id: "fake", aio: &audio.IO{Triggers: triggers, Mic: chanMic{}, Speaker: sp}}
	_, buf := startSync(t, b, openStore(t))
	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle, Test: true}
	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	deadline := time.Now().Add(2 * time.Second)
	for strings.Count(buf.String(), `"msg":"activation_refused"`) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sp.played(); len(got) != 1 || got[0] != "offline/en" {
		t.Fatalf("played %v; log: %s", got, buf.String())
	}
	if n := strings.Count(buf.String(), `"msg":"error_prompt","id":"offline","sound":"prompt"`); n != 1 {
		t.Fatalf("%d error_prompt lines: %s", n, buf.String())
	}
}
