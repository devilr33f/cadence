package input_test

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

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type fakeButtons chan input.ButtonEvent

func (b fakeButtons) Events() <-chan input.ButtonEvent { return b }

type fakeLights struct {
	mu    sync.Mutex
	muted []bool
	shown []int
}

func (l *fakeLights) Muted(m bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.muted = append(l.muted, m)
}

func (l *fakeLights) ShowVolume(level int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.shown = append(l.shown, level)
}

func (l *fakeLights) seen() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return fmt.Sprintf("muted %v shown %v", l.muted, l.shown)
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	in      *input.Input
	buttons fakeButtons
	lights  *fakeLights
	logs    *syncBuffer
	vol     *volume.Setter
	dir     string

	mu    sync.Mutex
	clock time.Time
}

func newHarness(t *testing.T, o input.Options) *harness {
	t.Helper()
	h := &harness{buttons: make(fakeButtons), lights: &fakeLights{}, logs: &syncBuffer{}, dir: t.TempDir(),
		clock: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	if err := volume.NewStore(h.dir).Save(50); err != nil {
		t.Fatal(err)
	}
	h.vol = volume.NewSetter(volume.NewStore(h.dir), 30, nil)
	o.Buttons, o.Volume, o.Log = h.buttons, h.vol, slog.New(slog.NewTextHandler(h.logs, nil))
	o.Now = h.now
	h.in = input.New(o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.in.Run(ctx, h.lights) }()
	t.Cleanup(func() { cancel(); <-done })
	return h
}

func (h *harness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = h.clock.Add(d)
}

func (h *harness) press(k input.Kind) {
	h.buttons <- input.ButtonEvent{Kind: k, Pressed: true, At: h.now()}
	h.buttons <- input.ButtonEvent{Kind: k, Pressed: false, At: h.now()}
}

// flip sets the mute switch; Run has taken the event once a later one is sent
func (h *harness) flip(k input.Kind) {
	h.buttons <- input.ButtonEvent{Kind: k, At: h.now()}
}

// took returns once Run acted on the event sent before it: a volume release changes nothing
func (h *harness) took() {
	h.buttons <- input.ButtonEvent{Kind: input.VolumeDown, Pressed: false}
}

// settle returns once Run took everything sent before it: a release changes nothing
func (h *harness) settle() {
	h.buttons <- input.ButtonEvent{Kind: input.Action, Pressed: false}
	h.buttons <- input.ButtonEvent{Kind: input.Action, Pressed: false}
}

func (h *harness) trigger(t *testing.T) audio.Trigger {
	t.Helper()
	select {
	case tr := <-h.in.Triggers():
		return tr
	case <-time.After(2 * time.Second):
		t.Fatal("no trigger")
		return audio.Trigger{}
	}
}

func (h *harness) noTrigger(t *testing.T, what string) {
	t.Helper()
	select {
	case tr := <-h.in.Triggers():
		t.Fatalf("%s: trigger %+v", what, tr)
	case <-time.After(30 * time.Millisecond):
	}
}

func (h *harness) envelope(body any) {
	env := &audiodv1.Envelope{}
	switch b := body.(type) {
	case *audiodv1.WakeEvent:
		env.Body = &audiodv1.Envelope_WakeEvent{WakeEvent: b}
	case *audiodv1.CommandWord:
		env.Body = &audiodv1.Envelope_CommandWord{CommandWord: b}
	case *audiodv1.VendorStatus:
		env.Body = &audiodv1.Envelope_VendorStatus{VendorStatus: b}
	}
	h.in.InputEnvelope(env)
}

func (h *harness) wake() {
	h.envelope(&audiodv1.WakeEvent{Engine: "stock", Score: 0.8, AtMs: h.now().UnixMilli()})
}

func (h *harness) state(t *testing.T) string {
	t.Helper()
	select {
	case s := <-h.in.Capability().State():
		return string(s)
	case <-time.After(2 * time.Second):
		t.Fatal("no state")
		return ""
	}
}

// audiod is what InputReady binds: every MuteState it was sent
type audiod struct{ sent chan bool }

func ready(h *harness) *audiod {
	a := &audiod{sent: make(chan bool, 16)}
	h.in.InputReady(func(env *audiodv1.Envelope) error {
		a.sent <- env.GetMuteState().GetMuted()
		return nil
	})
	return a
}

func (a *audiod) muted(t *testing.T) bool {
	t.Helper()
	select {
	case m := <-a.sent:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no MuteState")
		return false
	}
}

func (a *audiod) none(t *testing.T, what string) {
	t.Helper()
	select {
	case m := <-a.sent:
		t.Fatalf("%s: MuteState{%v}", what, m)
	default:
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestActionPressTogglesASession(t *testing.T) {
	h := newHarness(t, input.Options{})
	h.press(input.Action)
	if tr := h.trigger(t); tr.Kind != cadencev2.Trigger_TRIGGER_BUTTON || tr.Mode != audio.Toggle || tr.Wake != nil {
		t.Fatalf("trigger = %+v", tr)
	}
	h.settle()
	h.noTrigger(t, "the release")
}

// rule 7: a press may raise; it steps 5 from the level in force, is kept, and shows on the lights
func TestVolumeButtonsStepAndShow(t *testing.T) {
	h := newHarness(t, input.Options{})
	var mu sync.Mutex
	var origins []volume.Origin
	h.vol.Listen(func(_ int, o volume.Origin) {
		mu.Lock()
		defer mu.Unlock()
		origins = append(origins, o)
	})
	h.press(input.VolumeUp)
	h.press(input.VolumeDown)
	h.press(input.VolumeDown)
	h.settle()
	if got := h.lights.seen(); got != "muted [] shown [55 50 45]" {
		t.Fatalf("lights %s", got)
	}
	if level, ok := volume.NewStore(h.dir).Load(); !ok || level != 45 {
		t.Fatalf("stored %d, %v", level, ok)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(origins) != "[button button button]" {
		t.Fatalf("origins %v", origins)
	}
	h.noTrigger(t, "volume presses")
}

func TestMuteSwitch(t *testing.T) {
	h := newHarness(t, input.Options{})
	if s := h.state(t); s != `{"muted":false,"wake":false}` {
		t.Fatalf("state %s", s)
	}
	a := ready(h)
	if a.muted(t) {
		t.Fatal("first MuteState muted")
	}
	h.flip(input.MuteOn)
	if !a.muted(t) {
		t.Fatal("mute_on sent no MuteState{true}")
	}
	if tr := h.trigger(t); tr.Mode != audio.Mute {
		t.Fatalf("trigger = %+v", tr)
	}
	if s := h.state(t); s != `{"muted":true,"wake":false}` {
		t.Fatalf("state %s", s)
	}
	// the switch reports its position: the same one again changes nothing
	h.flip(input.MuteOn)
	h.settle()
	a.none(t, "a repeated mute_on")
	h.noTrigger(t, "a repeated mute_on")

	// the action button starts nothing while muted (v1): audiod lets no microphone frame out
	h.press(input.Action)
	h.settle()
	h.noTrigger(t, "a press while muted")
	if !strings.Contains(h.logs.String(), "msg=activation_refused reason=muted source=button") {
		t.Fatalf("log: %s", h.logs)
	}

	// audiod forgets the switch with its client: a restarted one hears it at once
	h.in.InputGone()
	restarted := ready(h)
	if !restarted.muted(t) {
		t.Fatal("a restarted audiod was not told the switch is on")
	}
	h.flip(input.MuteOff)
	if restarted.muted(t) {
		t.Fatal("mute_off sent no MuteState{false}")
	}
	h.settle()
	a.none(t, "the old connection")
	h.noTrigger(t, "an unmute")
	if got := h.lights.seen(); got != "muted [true false] shown []" {
		t.Fatalf("lights %s", got)
	}
	h.press(input.Action)
	if tr := h.trigger(t); tr.Mode != audio.Toggle {
		t.Fatalf("press after unmute: %+v", tr)
	}
}

func TestWakeBeginsASession(t *testing.T) {
	h := newHarness(t, input.Options{})
	ready(h)
	h.envelope(&audiodv1.VendorStatus{Engines: true})
	h.advance(3 * time.Second)
	h.wake()
	tr := h.trigger(t)
	if tr.Kind != cadencev2.Trigger_TRIGGER_WAKE || tr.Mode != audio.IfIdle || tr.Wake.GetEngine() != "stock" || tr.Wake.GetScore() != 0.8 {
		t.Fatalf("trigger = %+v", tr)
	}
	if s := h.state(t); s != `{"muted":false,"wake":true}` {
		t.Fatalf("state %s", s)
	}
}

func TestWakeIgnored(t *testing.T) {
	h := newHarness(t, input.Options{})
	ready(h)
	ignored := func(reason string) {
		t.Helper()
		before := strings.Count(h.logs.String(), "msg=wake_ignored reason="+reason)
		h.wake()
		h.noTrigger(t, "wake "+reason)
		if strings.Count(h.logs.String(), "msg=wake_ignored reason="+reason) != before+1 {
			t.Fatalf("no wake_ignored reason=%s: %s", reason, h.logs)
		}
	}
	heard := func(what string) {
		t.Helper()
		h.wake()
		if tr := h.trigger(t); tr.Mode != audio.IfIdle {
			t.Fatalf("%s: %+v", what, tr)
		}
	}

	// before audiod says the engines run
	ignored("unavailable")
	// the engines just opened the capture: v1's 3 s after a reopen
	h.envelope(&audiodv1.VendorStatus{Engines: true})
	h.advance(2999 * time.Millisecond)
	ignored("reopen")
	h.advance(time.Millisecond)
	heard("3 s after the engines started")

	h.flip(input.MuteOn)
	if tr := h.trigger(t); tr.Mode != audio.Mute {
		t.Fatalf("mute trigger = %+v", tr)
	}
	ignored("muted")
	// an unmute starts the engines' warm-up again, as a reopen does
	h.flip(input.MuteOff)
	h.settle()
	h.advance(2 * time.Second)
	ignored("reopen")
	h.advance(time.Second)
	heard("3 s after the unmute")

	// engines off, or a spotter that failed: nothing wakes, the button still works
	for _, vs := range []*audiodv1.VendorStatus{{Engines: false, Code: "probe_failed"}, {Engines: true, Code: "spotter_failed"}} {
		h.envelope(vs)
		h.advance(time.Minute)
		ignored("unavailable")
		if s := h.state(t); s != `{"muted":false,"wake":false}` {
			t.Fatalf("state after %v: %s", vs, s)
		}
		h.press(input.Action)
		if tr := h.trigger(t); tr.Mode != audio.Toggle {
			t.Fatalf("press without wake: %+v", tr)
		}
	}
	// command words alone off: the wake still works
	h.envelope(&audiodv1.VendorStatus{Engines: true, Code: "command_model_missing"})
	h.advance(3 * time.Second)
	heard("without command words")

	// audiod gone, and back until its engines report
	h.in.InputGone()
	ignored("unavailable")
	ready(h)
	h.advance(time.Minute)
	ignored("unavailable")
}

// policy acts on word 3 and ignores the rest, keeping what it was told
type policy struct {
	mu    sync.Mutex
	heard []string
}

func (p *policy) Command(id uint32, muted bool) (action, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.heard = append(p.heard, fmt.Sprintf("%d muted=%v", id, muted))
	if id == 3 {
		return "volume_down", ""
	}
	return "", "server"
}

func (p *policy) told() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.heard)
}

// the policy decides each word, told whether the switch mutes the microphone; the input counts what it acted on and
// what it ignored, by id: the phrase never crosses here
func TestCommandWordsGoToThePolicyAndAreCounted(t *testing.T) {
	h := newHarness(t, input.Options{})
	h.envelope(&audiodv1.CommandWord{Id: 3, Score: 1})
	if out := h.logs.String(); !strings.Contains(out, "msg=command_ignored id=3 reason=unavailable count=1") {
		t.Fatalf("log: %s", out)
	}

	p := &policy{}
	h.in.UseCommands(p)
	h.envelope(&audiodv1.CommandWord{Id: 3, Score: 1})
	h.envelope(&audiodv1.CommandWord{Id: 1, Score: 1})
	h.flip(input.MuteOn)
	h.settle()
	if tr := h.trigger(t); tr.Mode != audio.Mute {
		t.Fatalf("switch = %+v", tr)
	}
	h.envelope(&audiodv1.CommandWord{Id: 0})
	out := h.logs.String()
	for _, want := range []string{
		"msg=command id=3 action=volume_down count=1",
		"msg=command_ignored id=1 reason=server count=2",
		"msg=command_ignored id=0 reason=server count=3",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in log: %s", want, out)
		}
	}
	if got := p.told(); !slices.Equal(got, []string{"3 muted=false", "1 muted=false", "0 muted=true"}) {
		t.Fatalf("told %v", got)
	}
	for _, word := range input.CommandWords() {
		if strings.Contains(out, word) {
			t.Fatalf("a phrase in the log: %s", out)
		}
	}
	h.noTrigger(t, "command words")
}

// what the switch says, for a follow-up's facts
func TestMutedFollowsTheSwitch(t *testing.T) {
	h := newHarness(t, input.Options{})
	if h.in.Muted() {
		t.Fatal("muted at rest")
	}
	h.flip(input.MuteOn)
	h.settle()
	if !h.in.Muted() {
		t.Fatal("not muted with the switch on")
	}
	h.flip(input.MuteOff)
	h.settle()
	if h.in.Muted() {
		t.Fatal("muted with the switch off")
	}
}

type ringer struct {
	mu      sync.Mutex
	ringing bool
	stops   []input.Kind
	snoozes int
}

func (r *ringer) SnoozeStopped() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snoozes++
}

func (r *ringer) snoozed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snoozes
}

func (r *ringer) Ringing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ringing
}

func (r *ringer) StopRing(by input.Kind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops = append(r.stops, by)
}

// M6: while a ring sounds, a press only stops it
func TestPressWhileRingingStopsTheRing(t *testing.T) {
	r := &ringer{ringing: true}
	h := newHarness(t, input.Options{Ringer: r})
	h.press(input.Action)
	h.press(input.VolumeUp)
	h.settle()
	h.noTrigger(t, "a press that stops a ring")
	if h.vol.Level() != 50 || h.lights.seen() != "muted [] shown []" {
		t.Fatalf("level %d, lights %s", h.vol.Level(), h.lights.seen())
	}
	r.mu.Lock()
	stops := slices.Clone(r.stops)
	r.ringing = false
	r.mu.Unlock()
	if !slices.Equal(stops, []input.Kind{input.Action, input.VolumeUp}) {
		t.Fatalf("stops %v", stops)
	}
	h.press(input.Action)
	if tr := h.trigger(t); tr.Mode != audio.Toggle {
		t.Fatalf("press after the ring: %+v", tr)
	}
}

// owner 2026-11-16: the action button held down a second from the press that stopped a ring snoozes it; a short
// press, a held volume button and a hold that stopped nothing do not
func TestALongPressSnoozesTheRingItStopped(t *testing.T) {
	r := &ringer{ringing: true}
	h := newHarness(t, input.Options{Ringer: r})
	hold := func(k input.Kind, d time.Duration) {
		h.buttons <- input.ButtonEvent{Kind: k, Pressed: true, At: h.now()}
		h.took()
		h.advance(d)
		h.buttons <- input.ButtonEvent{Kind: k, Pressed: false, At: h.now()}
		h.settle()
	}
	hold(input.Action, input.LongPress-time.Millisecond)
	if n := r.snoozed(); n != 0 {
		t.Fatalf("a short press snoozed %d", n)
	}
	hold(input.VolumeUp, 2*input.LongPress)
	if n := r.snoozed(); n != 0 {
		t.Fatalf("a held volume button snoozed %d", n)
	}
	hold(input.Action, input.LongPress)
	if n := r.snoozed(); n != 1 {
		t.Fatalf("a long press snoozed %d", n)
	}
	r.mu.Lock()
	r.ringing = false
	r.mu.Unlock()
	hold(input.Action, 2*input.LongPress)
	if n := r.snoozed(); n != 1 {
		t.Fatalf("a hold with nothing ringing snoozed %d", n)
	}
	if tr := h.trigger(t); tr.Mode != audio.Toggle {
		t.Fatalf("the press after the ring: %+v", tr)
	}
}

func TestCapabilityTakesNoConfig(t *testing.T) {
	p := input.New(input.Options{}).Capability()
	if d := p.Descriptor(); d.ID != "audio.input" || d.Version != 1 {
		t.Fatalf("descriptor %+v", d)
	}
	if err := p.Apply(context.Background(), 1, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("apply {}: %v", err)
	}
	for _, bad := range []string{`{"muted":true}`, `[]`, `{} {}`, ``} {
		if err := p.Apply(context.Background(), 1, json.RawMessage(bad)); err == nil {
			t.Fatalf("apply %q accepted", bad)
		}
	}
	eventually(t, "a state", func() bool { return len(p.State()) == 1 })
}

// the board reads the switch before audio starts: the first MuteState any audiod gets is the real position
func TestStartsInTheSwitchPosition(t *testing.T) {
	h := newHarness(t, input.Options{Muted: true})
	if s := h.state(t); s != `{"muted":true,"wake":false}` {
		t.Fatalf("state %s", s)
	}
	a := ready(h)
	if !a.muted(t) {
		t.Fatal("the first MuteState was not muted")
	}
	h.envelope(&audiodv1.VendorStatus{Engines: true})
	h.advance(time.Minute)
	h.wake()
	h.noTrigger(t, "a wake while muted from the start")
	h.press(input.Action)
	// the board's own first report of the position repeats it
	h.flip(input.MuteOn)
	h.settle()
	h.noTrigger(t, "a press while muted from the start")
	a.none(t, "the repeated position")
	if got := h.lights.seen(); got != "muted [true] shown []" {
		t.Fatalf("lights %s", got)
	}
}

// the switch is never dropped: activations still queued were made before it and go, so it always finds room
func TestMuteNeverDroppedFromAFullQueue(t *testing.T) {
	h := newHarness(t, input.Options{})
	for range 5 {
		h.press(input.Action)
	}
	h.settle()
	if !strings.Contains(h.logs.String(), "msg=trigger_dropped trigger=TRIGGER_BUTTON reason=full") {
		t.Fatalf("log: %s", h.logs)
	}
	h.flip(input.MuteOn)
	h.settle()
	if tr := h.trigger(t); tr.Mode != audio.Mute {
		t.Fatalf("first queued = %+v", tr)
	}
	h.noTrigger(t, "after the switch")
	if !strings.Contains(h.logs.String(), "msg=trigger_dropped trigger=TRIGGER_BUTTON reason=muted") {
		t.Fatalf("log: %s", h.logs)
	}
}

// a wake's trigger starts at the instant audiod heard it, nanoseconds when it sends them; one from another clock is now
func TestWakeTriggerCarriesItsInstant(t *testing.T) {
	h := newHarness(t, input.Options{})
	ready(h)
	h.envelope(&audiodv1.VendorStatus{Engines: true})
	h.advance(5 * time.Second)
	now := h.now()
	for _, c := range []struct {
		w    *audiodv1.WakeEvent
		want time.Time
	}{
		{&audiodv1.WakeEvent{AtMs: now.Add(-time.Second).UnixMilli()}, time.UnixMilli(now.Add(-time.Second).UnixMilli())},
		{&audiodv1.WakeEvent{AtMs: 1, AtUnixNs: now.Add(-time.Millisecond).UnixNano()}, time.Unix(0, now.Add(-time.Millisecond).UnixNano())},
		{&audiodv1.WakeEvent{AtMs: now.Add(-time.Hour).UnixMilli()}, now},
		{&audiodv1.WakeEvent{}, now},
	} {
		h.envelope(c.w)
		if got := h.trigger(t).At; !got.Equal(c.want) {
			t.Fatalf("%v: at %v, want %v", c.w, got, c.want)
		}
	}
}
