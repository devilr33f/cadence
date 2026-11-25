package leds

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 9, 7, 42, 0, 0, time.UTC)

// written is one frame as the sink received it
type written struct {
	ring, button []RGB
	segments     map[Segment]uint8
}

type fakeSink struct {
	geo Geometry

	mu     sync.Mutex
	frames []written
	cur    written
	calls  int
	// fail makes the call number n (1-based, counted over every method) return an error
	fail map[int]error
	// gate, when set, blocks every Ring call until it is closed
	gate   chan struct{}
	lux    int
	hasLux bool
	notify chan struct{}
}

func newSink(g Geometry) *fakeSink { return &fakeSink{geo: g, notify: make(chan struct{}, 1024)} }

func (s *fakeSink) Geometry() Geometry { return s.geo }

func (s *fakeSink) next() error {
	s.calls++
	return s.fail[s.calls]
}

func (s *fakeSink) Ring(px []RGB) error {
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = written{ring: append([]RGB(nil), px...)}
	if s.geo.Button == 0 && s.geo.Digits == 0 {
		s.finish()
	}
	return s.next()
}

func (s *fakeSink) Button(px []RGB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur.button = append([]RGB(nil), px...)
	if s.geo.Digits == 0 {
		s.finish()
	}
	return s.next()
}

func (s *fakeSink) Segments(level map[Segment]uint8) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur.segments = map[Segment]uint8{}
	for k, v := range level {
		s.cur.segments[k] = v
	}
	s.finish()
	return s.next()
}

// a frame ends at its last call; a board without a clock ends it at the button or the ring
func (s *fakeSink) finish() {
	s.frames = append(s.frames, s.cur)
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *fakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func (s *fakeSink) last() written {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames[len(s.frames)-1]
}

type lightSink struct{ *fakeSink }

func (l lightSink) Light() (int, bool) { return l.lux, l.hasLux }

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newRenderer(t *testing.T, sink Sink, clk *clock) (*Renderer, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	r, err := New(Options{Sink: sink, Log: slog.New(slog.NewTextHandler(&logs, nil)), Now: clk.Now, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	return r, &logs
}

func ringPeak(w written) int {
	p := 0
	for _, px := range w.ring {
		p = max(p, peak(px))
	}
	return p
}

func TestNewRejectsAnImpossibleGeometry(t *testing.T) {
	for _, g := range []Geometry{{Ring: -1}, {Digits: 3, Segments: 8}, {Digits: 4, Segments: 6}} {
		if _, err := New(Options{Sink: newSink(g), Now: time.Now}); !errors.Is(err, ErrGeometry) {
			t.Fatalf("geometry %+v: err = %v", g, err)
		}
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("no sink must fail")
	}
}

func TestStepWritesTheIdleClock(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	if !r.step(clk.now) {
		t.Fatal("step failed")
	}
	w := sink.last()
	// no light sensor: brightness 0.6, so a lit segment is 120
	if len(w.ring) != 16 || len(w.button) != 6 || ringPeak(w) != 0 {
		t.Fatalf("idle ring/button: %+v", w)
	}
	want := map[Segment]uint8{Colon: 120}
	for d, row := range lit(timeGlyphs(7, 42), 120) {
		for s := 0; s < 7; s++ {
			want[Segment{d, s}] = row[s]
		}
	}
	want[Degree] = 0
	if !reflect.DeepEqual(w.segments, want) {
		t.Fatalf("segments = %v", w.segments)
	}
}

func TestClockUsesTheLocalZoneAndWaitsForASaneYear(t *testing.T) {
	sink := newSink(round)
	clk := &clock{time.Date(2026, 11, 9, 23, 30, 0, 0, time.UTC)}
	var logs bytes.Buffer
	zone := time.FixedZone("x", 3*3600)
	r, err := New(Options{Sink: sink, Log: slog.New(slog.NewTextHandler(&logs, nil)), Now: clk.Now, Location: zone})
	if err != nil {
		t.Fatal(err)
	}
	r.step(clk.now)
	if got := sink.last().segments[Segment{0, 3}]; got != 120 || sink.last().segments[Segment{0, 5}] != 120 {
		// 02:30 -> digit 0 is "0": segments d and f lit
		t.Fatalf("zone not applied, digit 0 = %v", sink.last().segments)
	}
	clk.now = time.Date(1970, 1, 1, 0, 0, 5, 0, time.UTC)
	r.step(clk.now)
	if sink.last().segments[Segment{0, 6}] != 120 || sink.last().segments[Colon] != 0 {
		t.Fatal("an unsynced clock shows dashes")
	}
}

func TestStateChangeCrossfadesOver200ms(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.step(clk.now)
	r.SetState(Thinking)
	r.step(clk.now)
	if ringPeak(sink.last()) != 0 {
		t.Fatal("the fade starts from what shows")
	}
	clk.advance(100 * time.Millisecond)
	r.step(clk.now)
	mid := ringPeak(sink.last())
	clk.advance(100 * time.Millisecond)
	r.step(clk.now)
	full := sink.last()
	want := Render(round, Input{State: Thinking, Since: ms(200), Clock: ms(200), Time: &ClockTime{7, 42}, Brightness: 0.6})
	if !reflect.DeepEqual(full.ring, want.Ring) {
		t.Fatalf("after the fade the ring is the state's: %v vs %v", full.ring, want.Ring)
	}
	if mid <= 0 || mid >= ringPeak(full) {
		t.Fatalf("midpoint peak %d not between 0 and %d", mid, ringPeak(full))
	}
}

func TestRepeatedStateKeepsItsAnimationButOfflineBlinksAgain(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.SetState(Offline)
	clk.advance(800 * time.Millisecond)
	r.SetState(Offline)
	r.step(clk.now)
	if ringPeak(sink.last()) != 0 {
		t.Fatal("a repeated offline starts its blink over, from what shows (dark)")
	}
	clk.advance(100 * time.Millisecond)
	r.step(clk.now)
	if ringPeak(sink.last()) == 0 {
		t.Fatal("the blink did not restart")
	}
	r.SetState(Thinking)
	clk.advance(time.Second)
	r.step(clk.now)
	before := sink.last().ring
	r.SetState(Thinking)
	r.step(clk.now)
	if !reflect.DeepEqual(before, sink.last().ring) {
		t.Fatal("a repeated state must not restart the fade")
	}
}

func TestVolumeOverlayFadesGlidesHoldsAndLeaves(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.step(clk.now)
	idle := sink.last()

	r.ShowVolume(50)
	clk.advance(75 * time.Millisecond)
	r.step(clk.now)
	half := sink.last()
	// opacity 0.5 after half the 150 ms fade-in, and the fill starts at its level: led 7 lit, led 8 not
	// digit 2 of 07:42 is a 4 (no d); the level 50 has a 5 there (d lit), at half opacity
	if half.segments[Segment{2, 3}] != 60 {
		t.Fatalf("level digits are fading in: %v", half.segments)
	}
	if peak(half.ring[7]) == 0 || peak(half.ring[8]) != 0 {
		t.Fatalf("fill edge: %v %v", half.ring[7], half.ring[8])
	}
	clk.advance(75 * time.Millisecond)
	r.step(clk.now)
	want := Render(round, Input{State: Idle, Time: &ClockTime{7, 42}, Brightness: 0.6,
		Overlays: []VolumeOverlay{{Level: 50, Fill: 0.5, Opacity: 1}}})
	if got := sink.last().ring; !reflect.DeepEqual(got, want.Ring) {
		t.Fatalf("full overlay ring = %v", got)
	}
	// held 2 s after the press, fading out over 250 ms after that
	clk.advance(1850 * time.Millisecond)
	r.step(clk.now)
	if !reflect.DeepEqual(sink.last().ring, want.Ring) {
		t.Fatal("overlay must hold for 2 s")
	}
	clk.advance(125 * time.Millisecond)
	r.step(clk.now)
	if reflect.DeepEqual(sink.last().ring, want.Ring) {
		t.Fatal("overlay must fade out")
	}
	clk.advance(300 * time.Millisecond)
	r.step(clk.now)
	if !reflect.DeepEqual(sink.last().ring, idle.ring) || !reflect.DeepEqual(sink.last().segments, idle.segments) {
		t.Fatal("overlay gone")
	}
}

func TestVolumeFillGlidesToANewLevel(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.ShowVolume(25)
	r.step(clk.now)
	clk.advance(200 * time.Millisecond)
	r.step(clk.now)
	r.ShowVolume(75)
	clk.advance(33 * time.Millisecond)
	r.step(clk.now)
	lit := 0
	for _, px := range sink.last().ring {
		if px != (RGB{}) {
			lit++
		}
	}
	// the fill left 4 leds and is on its way to 12
	if lit <= 4 || lit >= 12 {
		t.Fatalf("lit %d, want a glide between 4 and 12", lit)
	}
}

func TestSwitchesMaskWhatIsWrittenAndRedrawOnTurnOn(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.SetState(Listening)
	clk.advance(time.Second)
	r.step(clk.now)
	r.ShowVolume(40)
	clk.advance(200 * time.Millisecond)
	r.step(clk.now)
	if w := sink.last(); ringPeak(w) == 0 || w.segments[Segment{3, 0}] == 0 {
		t.Fatalf("setup: ring and overlay digits must show: %+v", w)
	}

	r.SetSwitches(false, true)
	r.step(clk.now)
	w := sink.last()
	if ringPeak(w) != 0 || peak(w.button[0]) != 0 {
		t.Fatal("lights off blanks ring and button at once, the volume overlay included")
	}
	if w.segments[Segment{3, 0}] == 0 {
		t.Fatal("clock stays on while only the lights are off")
	}

	r.SetSwitches(true, false)
	r.step(clk.now)
	w = sink.last()
	if ringPeak(w) == 0 {
		t.Fatal("lights back on redraw the state that ran all along")
	}
	for k, v := range w.segments {
		if v != 0 {
			t.Fatalf("clock off must blank segment %v", k)
		}
	}
}

func TestReplyLevelSwellsTheSpeakingRingSmoothed(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.SetState(Speaking)
	clk.advance(time.Second)
	r.step(clk.now)
	quiet := ringPeak(sink.last())
	r.SetReplyLevel(1)
	clk.advance(33 * time.Millisecond)
	r.step(clk.now)
	attack := ringPeak(sink.last())
	for range 10 {
		r.SetReplyLevel(1)
		clk.advance(33 * time.Millisecond)
		r.step(clk.now)
	}
	if attack <= quiet || ringPeak(sink.last()) <= attack {
		t.Fatalf("level must swell the ring over frames: %d %d %d", quiet, attack, ringPeak(sink.last()))
	}
	// the swell decays once the reply goes quiet
	clk.advance(5 * time.Second)
	r.step(clk.now)
	want := Render(round, Input{State: Speaking, Since: 6 * time.Second, Clock: clk.now.Sub(t0), Time: &ClockTime{7, 42}, Brightness: 0.6})
	if !reflect.DeepEqual(sink.last().ring, want.Ring) {
		t.Fatal("level did not release")
	}
}

func TestMicLevelSwellsTheListeningRing(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.SetState(Listening)
	clk.advance(time.Second)
	r.step(clk.now)
	for range 10 {
		r.SetMicLevel(1)
		clk.advance(33 * time.Millisecond)
		r.step(clk.now)
	}
	quiet := Render(round, Input{State: Listening, Since: 1330 * time.Millisecond, Clock: clk.now.Sub(t0), Time: &ClockTime{7, 42}, Brightness: 0.6})
	if ringPeak(sink.last()) <= ringPeak(written{ring: quiet.Ring}) {
		t.Fatal("mic must swell the listening ring")
	}
}

// the microphone stays open during playback, so its voice and the reply's echo must never move the speaking meter
func TestEachStateFollowsOnlyItsOwnAudio(t *testing.T) {
	for _, tc := range []struct {
		state State
		other func(*Renderer, float64)
	}{
		{Speaking, (*Renderer).SetMicLevel},
		{Listening, (*Renderer).SetReplyLevel},
		{Idle, (*Renderer).SetMicLevel},
		{Idle, (*Renderer).SetReplyLevel},
	} {
		sink, clk := newSink(round), &clock{t0}
		r, _ := newRenderer(t, sink, clk)
		r.SetState(tc.state)
		clk.advance(time.Second)
		r.step(clk.now)
		for range 10 {
			tc.other(r, 1)
			clk.advance(33 * time.Millisecond)
			r.step(clk.now)
		}
		want := Render(round, Input{State: tc.state, Since: 1330 * time.Millisecond, Clock: clk.now.Sub(t0), Time: &ClockTime{7, 42}, Brightness: 0.6})
		if !reflect.DeepEqual(sink.last().ring, want.Ring) {
			t.Fatalf("state %d moved by the other audio", tc.state)
		}
	}
}

func TestStaleOffersFromBeforeAStateChangeNeverShow(t *testing.T) {
	for _, offer := range []func(*Renderer, float64){(*Renderer).SetMicLevel, (*Renderer).SetReplyLevel} {
		sink, clk := newSink(round), &clock{t0}
		r, _ := newRenderer(t, sink, clk)
		r.SetState(Listening)
		offer(r, 1)
		r.SetState(Speaking)
		offer(r, 1)
		r.SetState(Listening)
		clk.advance(time.Second)
		r.step(clk.now)
		want := Render(round, Input{State: Listening, Since: time.Second, Clock: time.Second, Time: &ClockTime{7, 42}, Brightness: 0.6})
		if !reflect.DeepEqual(sink.last().ring, want.Ring) {
			t.Fatal("an offer from before the state change showed")
		}
	}
}

func TestAlarmDotAndTimerCountdown(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.SetAlarms(true, clk.now.Add(90*time.Second))
	r.step(clk.now)
	w := sink.last()
	want := lit(countdownGlyphs(90), 120)
	for d := range want {
		for s := 0; s < 7; s++ {
			if w.segments[Segment{d, s}] != want[d][s] {
				t.Fatalf("countdown digit %d seg %d = %d", d, s, w.segments[Segment{d, s}])
			}
		}
	}
	if w.segments[Degree] != 120 || w.segments[Colon] != 120 {
		t.Fatal("dot and colon")
	}
	clk.advance(30 * time.Second)
	r.step(clk.now)
	want = lit(countdownGlyphs(60), 120)
	for d := range want {
		for s := 0; s < 7; s++ {
			if sink.last().segments[Segment{d, s}] != want[d][s] {
				t.Fatalf("60 s left is 1:00: digit %d seg %d", d, s)
			}
		}
	}
	r.SetAlarms(false, time.Time{})
	r.step(clk.now)
	if sink.last().segments[Degree] != 0 {
		t.Fatal("dot off")
	}
}

func TestBrightnessFollowsTheSensor(t *testing.T) {
	clk := &clock{t0}
	dim := lightSink{newSink(round)}
	dim.hasLux, dim.lux = true, 0
	r, _ := newRenderer(t, dim, clk)
	r.step(clk.now)
	// raw 0 is the dimmest room, 0.1: a lit segment is 200 * 0.1
	if got := dim.last().segments[Colon]; got != 20 {
		t.Fatalf("dark room colon = %d", got)
	}
	bright := lightSink{newSink(round)}
	bright.hasLux, bright.lux = true, 400
	r2, _ := newRenderer(t, bright, clk)
	r2.step(clk.now)
	if got := bright.last().segments[Colon]; got != 200 {
		t.Fatalf("bright room colon = %d", got)
	}
	// a sensor that cannot be read counts as no sensor
	broken := lightSink{newSink(round)}
	r3, _ := newRenderer(t, broken, clk)
	r3.step(clk.now)
	if got := broken.last().segments[Colon]; got != 120 {
		t.Fatalf("no reading colon = %d", got)
	}
}

func TestSinkFailureBlanksOnceLogsOnceAndStops(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	sink.fail = map[int]error{4: &SinkError{Reason: "ring_write_failed"}}
	r, logs := newRenderer(t, sink, clk)
	r.SetState(Listening)
	clk.advance(time.Second)
	if !r.step(clk.now) { // calls 1..3
		t.Fatal("first frame must succeed")
	}
	clk.advance(33 * time.Millisecond)
	if r.step(clk.now) { // call 4 fails
		t.Fatal("a failing sink must end the renderer")
	}
	blank := sink.last()
	if ringPeak(blank) != 0 || peak(blank.button[0]) != 0 {
		t.Fatalf("lights must be blanked after a failure: %+v", blank)
	}
	for k, v := range blank.segments {
		if v != 0 {
			t.Fatalf("segment %v not blank", k)
		}
	}
	calls := sink.calls
	if r.step(clk.now) || sink.calls != calls {
		t.Fatal("nothing is written after a failure")
	}
	r.Stop()
	if sink.calls != calls {
		t.Fatal("stop must not blank again after a failure")
	}
	out := logs.String()
	if strings.Count(out, "leds_failed") != 1 || !strings.Contains(out, "reason=ring_write_failed") {
		t.Fatalf("log = %q", out)
	}
}

func TestAnUnknownSinkErrorLogsACodeNotItsText(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	sink.fail = map[int]error{1: errors.New("open /dev/secret: nope")}
	r, logs := newRenderer(t, sink, clk)
	r.step(clk.now)
	if strings.Contains(logs.String(), "secret") || !strings.Contains(logs.String(), "reason=sink_failed") {
		t.Fatalf("log = %q", logs.String())
	}
}

func TestBoardWithoutLightsWritesNothing(t *testing.T) {
	sink, clk := newSink(Geometry{}), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.SetState(Thinking)
	r.ShowVolume(30)
	r.SetSwitches(true, true)
	if !r.step(clk.now) {
		t.Fatal("step on a board with no lights is fine")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	r.Stop()
	if sink.calls != 0 {
		t.Fatalf("%d sink calls on a board with no lights", sink.calls)
	}
}

func TestRingOnlyBoardWritesOnlyTheRing(t *testing.T) {
	sink, clk := newSink(ringOnly), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.step(clk.now)
	if sink.count() != 1 || len(sink.last().ring) != 12 || sink.last().button != nil || sink.last().segments != nil {
		t.Fatalf("frame = %+v", sink.last())
	}
}

// fake pacing: Tick hands the loop a channel the test feeds
type ticks struct {
	ch   chan time.Time
	got  chan time.Duration
	done chan struct{}
}

func newTicks() *ticks {
	return &ticks{ch: make(chan time.Time), got: make(chan time.Duration, 1), done: make(chan struct{}, 1)}
}

func (k *ticks) tick(d time.Duration) (<-chan time.Time, func()) {
	k.got <- d
	return k.ch, func() { k.done <- struct{}{} }
}

func startLoop(t *testing.T, sink Sink, clk *clock, k *ticks) *Renderer {
	t.Helper()
	r, err := New(Options{Sink: sink, Now: clk.Now, Tick: k.tick, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	r.Start(context.Background())
	return r
}

func waitFrames(t *testing.T, s *fakeSink, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for s.count() < n {
		select {
		case <-s.notify:
		case <-deadline:
			t.Fatalf("only %d frames, want %d", s.count(), n)
		}
	}
}

func TestLoopPacesAt30FpsAndDrawsOnEveryTick(t *testing.T) {
	sink, clk, k := newSink(round), &clock{t0}, newTicks()
	r := startLoop(t, sink, clk, k)
	if d := <-k.got; d != 33*time.Millisecond {
		t.Fatalf("frame interval = %v", d)
	}
	for i := 1; i <= 3; i++ {
		clk.advance(33 * time.Millisecond)
		k.ch <- clk.now
		waitFrames(t, sink, i)
	}
	r.Stop()
	<-k.done
	// stopped with something drawn: one blank frame
	if ringPeak(sink.last()) != 0 || sink.last().segments[Colon] != 0 {
		t.Fatal("stop blanks the lights")
	}
	if sink.count() != 4 {
		t.Fatalf("frames = %d, want 3 plus the blank", sink.count())
	}
	r.Stop()
	if sink.count() != 4 {
		t.Fatal("a second stop must not write")
	}
}

func TestStopBeforeAnyFrameWritesNothing(t *testing.T) {
	sink, clk, k := newSink(round), &clock{t0}, newTicks()
	r := startLoop(t, sink, clk, k)
	<-k.got
	r.Stop()
	if sink.calls != 0 {
		t.Fatal("a renderer that never drew has nothing to clear")
	}
}

func TestUpdatesNeverBlockOnASlowSink(t *testing.T) {
	sink, clk, k := newSink(round), &clock{t0}, newTicks()
	sink.gate = make(chan struct{})
	r := startLoop(t, sink, clk, k)
	<-k.got
	k.ch <- clk.now // the loop is now stuck inside Ring
	returned := make(chan struct{})
	go func() {
		r.SetState(Thinking)
		r.ShowVolume(30)
		r.SetSwitches(false, false)
		r.SetMicLevel(0.5)
		r.SetReplyLevel(0.5)
		r.SetAlarms(true, clk.now.Add(time.Minute))
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("an update blocked behind the sink")
	}
	close(sink.gate)
	r.Stop()
}

// the zone the clock shows may come after the start: the device's timeZone setting arrives with the alarms' config
func TestTheClockTakesALaterZone(t *testing.T) {
	sink, clk := newSink(round), &clock{time.Date(2026, 11, 9, 23, 30, 0, 0, time.UTC)}
	r, _ := newRenderer(t, sink, clk)
	r.step(clk.now)
	if !reflect.DeepEqual(digitsOf(sink.last()), lit(timeGlyphs(23, 30), 120)) {
		t.Fatalf("UTC: %v", sink.last().segments)
	}
	r.SetLocation(time.FixedZone("x", 6*3600))
	r.step(clk.now)
	if !reflect.DeepEqual(digitsOf(sink.last()), lit(timeGlyphs(5, 30), 120)) {
		t.Fatalf("+06:00: %v", sink.last().segments)
	}
	r.SetLocation(nil)
	r.step(clk.now)
	if !reflect.DeepEqual(digitsOf(sink.last()), lit(timeGlyphs(5, 30), 120)) {
		t.Fatal("no zone changed the zone")
	}
}

func digitsOf(w written) [][]uint8 {
	out := make([][]uint8, 4)
	for d := range out {
		out[d] = make([]uint8, 8)
		for s := range 7 {
			out[d][s] = w.segments[Segment{d, s}]
		}
	}
	return out
}
