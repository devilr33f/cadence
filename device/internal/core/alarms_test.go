package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	_ "time/tzdata"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/session"
	"git.shino.boo/devilreef/cadence/device/internal/syncstore"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// shiftedClock runs at the real pace from a moment of the test's choosing
type shiftedClock struct {
	origin time.Time
	began  time.Time
}

func (c shiftedClock) Now() time.Time { return c.origin.Add(time.Since(c.began)) }

// tones is a speaker that sounds every ring it is asked for, at once
type tones struct {
	mu     sync.Mutex
	r      *alarms.Ringer
	called []string
}

func (s *tones) StartTone(id string, _ audiodv1.ToneKind) error {
	s.add("tone_start " + id)
	s.r.ToneState(&audiodv1.ToneState{Id: id, Sounding: true})
	return nil
}

func (s *tones) SetTransientLevel(level int) error {
	s.add(fmt.Sprintf("transient %d", level))
	return nil
}

func (s *tones) StopTone(id string) error {
	s.add("tone_stop " + id)
	return nil
}

func (s *tones) add(c string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.called = append(s.called, c)
}

func (s *tones) has(c string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.called, c)
}

// alarmBoard rings alarms and timers as a speaker does: the board builds the ringer over its volume and input
type alarmBoard struct {
	inputBoard
	ringer *alarms.Ringer
	recent *session.Recent
}

func (b alarmBoard) Alarms() *alarms.Ringer    { return b.ringer }
func (b alarmBoard) Sessions() *session.Recent { return b.recent }
func (b alarmBoard) Capabilities() []capability.Provider {
	return []capability.Provider{b.in.Capability(), b.ringer.Capability()}
}

var _ board.Alarms = alarmBoard{}

// until reads frames until one keeps; the ones before it are returned too
func (p *peer) until(keep func(*cadencev2.Envelope) bool) (*cadencev2.Envelope, []*cadencev2.Envelope) {
	p.t.Helper()
	var seen []*cadencev2.Envelope
	for {
		env := p.recv()
		if keep(env) {
			return env, seen
		}
		seen = append(seen, env)
	}
}

// an alarm due at boot rings offline by the zone kept from the last config; while it rings an injected utterance is
// refused and a press only stops it; a timer set over the link from a pressed session ends that session when it rings,
// and a wake stops the ring and begins its own session
func TestAlarmsRingOfflineAndOverTheLink(t *testing.T) {
	dir := t.TempDir()
	bishkek, err := time.LoadLocation("Asia/Bishkek")
	if err != nil {
		t.Fatal(err)
	}
	clock := shiftedClock{origin: time.Date(2026, 10, 26, 7, 0, 5, 0, bishkek), began: time.Now()}
	discard := slog.New(slog.DiscardHandler)
	// set the evening before, on a device that knew its zone
	before, err := alarms.NewBook(alarms.StorePath(dir), shiftedClock{origin: time.Date(2026, 10, 25, 22, 0, 0, 0, bishkek), began: time.Now()}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := before.SetZone("Asia/Bishkek"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := before.SetAlarm(alarms.Alarm{ID: "wake", Hour: 7, Date: alarms.Date{Year: 2026, Month: time.October, Day: 26}, Level: 80}); err != nil {
		t.Fatal(err)
	}
	store, err := syncstore.Open(syncstore.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(alarms.CapabilityID, 1, json.RawMessage(`{"timeZone":"Asia/Bishkek"}`)); err != nil {
		t.Fatal(err)
	}

	book, err := alarms.NewBook(alarms.StorePath(dir), clock, discard)
	if err != nil {
		t.Fatal(err)
	}
	vol := volume.NewSetter(volume.NewStore(dir), 30, discard)
	if _, err := vol.Set(30, volume.OriginButton); err != nil {
		t.Fatal(err)
	}
	vol.Follow(func(int) func(int) { return func(int) {} })
	recent := &session.Recent{}
	ringer := alarms.NewRinger(alarms.RingerOptions{Book: book, Volume: vol, Sessions: recent, Log: discard, Now: clock.Now})
	speaker := &tones{r: ringer}
	ringer.UseSpeaker(speaker)
	buttons := make(fakeButtons)
	b := alarmBoard{
		inputBoard: inputBoard{fakeBoard: fakeBoard{id: "speaker"}, in: input.New(input.Options{Buttons: buttons, Volume: vol, Ringer: ringer}),
			sink: &sink{}, say: make(chan audio.Trigger, 1)},
		ringer: ringer, recent: recent,
	}

	ls := newLinkServer(t)
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

	// no link yet: the alarm rings by Bishkek's calendar, five seconds late
	eventually(t, "the alarm ringing at boot", func() bool { return speaker.has("tone_start ring-1") })
	eventually(t, "the ring on the lights", b.sink.lit)
	press := func() {
		buttons <- input.ButtonEvent{Kind: input.Action, Pressed: true}
		buttons <- input.ButtonEvent{Kind: input.Action}
	}

	p := ls.accept(t)
	p.handshake(true)
	waitLog(t, buf, "link_up")
	b.say <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Fixture: "weather", Test: true}
	waitLog(t, buf, "activation_refused")
	if !strings.Contains(buf.String(), `"msg":"activation_refused","reason":"ringing","source":"inject"`) {
		t.Fatalf("log: %s", buf.String())
	}
	press()
	eventually(t, "the press stopping the ring", func() bool { return speaker.has("tone_stop ring-1") })
	eventually(t, "the lights at rest", func() bool { return !b.sink.lit() })
	if ringer.Ringing() {
		t.Fatal("ringing after the press")
	}

	press()
	start, _ := p.until(func(env *cadencev2.Envelope) bool { return env.GetSessionStart() != nil })
	id := start.GetSessionStart().GetSessionId()
	if start.GetSessionStart().GetTrigger() != cadencev2.Trigger_TRIGGER_BUTTON {
		t.Fatalf("start = %v", start)
	}
	p.command("r1", alarms.CapabilityID, "set_timer", fmt.Sprintf(`{"sessionId":%q,"id":"tea","seconds":1}`, id))
	res, _ := p.until(func(env *cadencev2.Envelope) bool { return env.GetCommandResult() != nil })
	if r := res.GetCommandResult(); r.GetCode() != "" || !strings.HasPrefix(r.GetJson(), `{"timer":{"id":"tea","seconds":1,`) {
		t.Fatalf("result = %v", r)
	}

	// the timer's ring ends the session the server holds, and the state says what rings
	cancelled, seen := p.until(func(env *cadencev2.Envelope) bool { return env.GetSessionCancel() != nil })
	if c := cancelled.GetSessionCancel(); c.GetSessionId() != id || c.GetReason() != "ring" {
		t.Fatalf("cancel = %v", c)
	}
	if !speaker.has("tone_start ring-2") {
		t.Fatal("the timer's tone did not start")
	}
	ringing := `"ringing":{"kind":"timer","id":"tea"}`
	if !slices.ContainsFunc(seen, func(env *cadencev2.Envelope) bool {
		d := env.GetStateDelta()
		return d.GetCapability() == alarms.CapabilityID && strings.Contains(d.GetJson(), ringing)
	}) {
		// the state may follow the cancel
		p.until(func(env *cadencev2.Envelope) bool {
			d := env.GetStateDelta()
			return d.GetCapability() == alarms.CapabilityID && strings.Contains(d.GetJson(), ringing)
		})
	}

	b.say <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	woken, _ := p.until(func(env *cadencev2.Envelope) bool { return env.GetSessionStart() != nil })
	if woken.GetSessionStart().GetTrigger() != cadencev2.Trigger_TRIGGER_WAKE {
		t.Fatalf("start = %v", woken)
	}
	// the session names the ring its wake stopped, so the server hears a request for more time as a snooze
	if ar := woken.GetSessionStart().GetAfterRing(); ar.GetKind() != "timer" || ar.GetId() != "tea" || ar.GetStoppedMsAgo() > 1000 {
		t.Fatalf("after_ring = %v", ar)
	}
	if !speaker.has("tone_stop ring-2") {
		t.Fatal("the wake left the ring sounding")
	}
}
