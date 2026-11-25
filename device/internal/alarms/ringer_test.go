package alarms_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

var errDown = errors.New("audiod down")

// calls is everything the ring did outside itself, in order: the speaker's tones, the hardware levels and core's hooks
type calls struct {
	mu   sync.Mutex
	list []string
}

func (c *calls) add(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, fmt.Sprintf(format, args...))
}

// take returns what happened since the last take
func (c *calls) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.list
	c.list = nil
	return out
}

type speaker struct {
	calls *calls
	mu    sync.Mutex
	err   error
}

func (s *speaker) StartTone(id string, kind audiodv1.ToneKind) error {
	s.calls.add("tone_start %s %s", id, strings.TrimPrefix(kind.String(), "TONE_KIND_"))
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *speaker) StopTone(id string) error {
	s.calls.add("tone_stop %s", id)
	return nil
}

func (s *speaker) SetTransientLevel(level int) error {
	s.calls.add("transient %d", level)
	return nil
}

func (s *speaker) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// rig is a ringer over a book in Bishkek, the real volume setter and a fake speaker, on a fake clock
type rig struct {
	*fixture
	book    *alarms.Book
	vol     *volume.Setter
	speaker *speaker
	calls   *calls
	r       *alarms.Ringer
	// the volume setter's own log, apart from the ring's and its key guard
	volLogs logBuf
}

// newRig starts with the level in force at level; a negative one leaves it unknown, the board's default. opts change
// the ringer's options
func newRig(t *testing.T, now time.Time, level int, opts ...func(*alarms.RingerOptions)) *rig {
	f := newFixture(t)
	g := &rig{fixture: f, book: f.open(now), calls: &calls{}}
	g.vol = volume.NewSetter(volume.NewStore(t.TempDir()), 30, slog.New(slog.NewTextHandler(&g.volLogs, nil)))
	if level >= 0 {
		if _, err := g.vol.Set(level, volume.OriginButton); err != nil {
			t.Fatal(err)
		}
	}
	g.vol.Follow(func(int) func(int) { return func(l int) { g.calls.add("plain %d", l) } })
	g.speaker = &speaker{calls: g.calls}
	o := alarms.RingerOptions{Book: g.book, Volume: g.vol, Log: slog.New(slog.NewJSONHandler(f.logs, nil)), Now: f.clock.Now}
	for _, opt := range opts {
		opt(&o)
	}
	g.r = alarms.NewRinger(o)
	g.r.UseSpeaker(g.speaker)
	alarms.Begin(g.r, alarms.Hooks{
		EndSession: func() { g.calls.add("end_session") },
		Ringing:    func(on bool) { g.calls.add("ringing %t", on) },
	})
	return g
}

// step is the ring loop waking at now; it returns when it would wake next
func (g *rig) step(now time.Time) time.Duration {
	g.clock.set(now)
	return alarms.Step(g.r, now)
}

func (g *rig) tone(id string, sounding bool, code string) {
	g.r.ToneState(&audiodv1.ToneState{Id: id, Sounding: sounding, Code: code})
}

// sound answers the ring's StartTone at now
func (g *rig) sound(id string, now time.Time) time.Duration {
	g.tone(id, true, "")
	return g.step(now)
}

func (g *rig) want(what string, want ...string) {
	g.t.Helper()
	got := g.calls.take()
	if want == nil {
		want = []string{}
	}
	if got == nil {
		got = []string{}
	}
	if !slices.Equal(got, want) {
		g.t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

func (g *rig) wantLevel(want int) {
	g.t.Helper()
	if level, _ := g.vol.Current(); level != want {
		g.t.Errorf("level in force %d, want %d", level, want)
	}
}

func (g *rig) wantEvents(msg string, want ...string) {
	g.t.Helper()
	var got []string
	for _, e := range g.logs.events(msg) {
		got = append(got, fmt.Sprintf("%v %v %v", e["kind"], e["id"], e["reason"]))
	}
	if !slices.Equal(got, want) {
		g.t.Errorf("%s = %q, want %q", msg, got, want)
	}
}

// 2026-10-26 is a Monday; Asia/Bishkek
func TestADueAlarmEndsTheSessionRingsAndRampsFromTheLevelInForce(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	g.mustSetAlarm(g.book, at(26, 6, 59, 0), alarms.Alarm{ID: "wake", Hour: 7, Days: weekdays, Level: 80})

	if next := g.step(at(26, 6, 59, 0)); next != time.Minute {
		t.Errorf("next wake %v, want a minute", next)
	}
	g.want("before it is due")
	if g.r.Ringing() {
		t.Fatal("ringing before it is due")
	}

	g.step(at(26, 7, 0, 0))
	g.want("a due alarm", "end_session", "ringing true", "tone_start ring-1 ALARM")
	if !g.r.Ringing() {
		t.Fatal("not ringing")
	}
	g.wantEvents("ring_start", "alarm wake <nil>")

	// audible half a second in: nothing earned yet, the first step 3 s after the ring began
	start := at(26, 7, 0, 0)
	if next := g.sound("ring-1", start.Add(500*time.Millisecond)); next != 2500*time.Millisecond {
		t.Errorf("next wake %v, want the first step", next)
	}
	g.want("audible")
	g.step(start.Add(3 * time.Second))
	g.want("3 s", "transient 35")
	// a wake that comes late catches up by time, one step
	g.step(start.Add(15 * time.Second))
	g.want("15 s", "transient 55")
	g.step(start.Add(30 * time.Second))
	g.want("30 s", "transient 80")
	g.step(start.Add(45 * time.Second))
	g.want("past the ramp")
	g.wantLevel(30)

	g.r.StopRing(input.Action)
	g.want("the button", "tone_stop ring-1", "plain 30", "ringing false")
	g.wantEvents("ring_stop", "alarm wake button")
	g.wantLevel(30)
	if g.r.Ringing() {
		t.Fatal("ringing after the stop")
	}
	// a ramp step can never come after the stop: nothing rings
	g.step(start.Add(46 * time.Second))
	g.want("after the stop")
}

// every stop ends the ramp, takes the tone back and only then puts the level in force back, once; a volume button
// steps nothing
func TestEveryStopRestoresOnceAfterTheToneStops(t *testing.T) {
	start := at(26, 7, 0, 0)
	for _, c := range []struct {
		reason string
		stop   func(g *rig)
	}{
		{"button", func(g *rig) { g.r.StopRing(input.Action) }},
		{"volume", func(g *rig) { g.r.StopRing(input.VolumeUp) }},
		{"volume", func(g *rig) { g.r.StopRing(input.VolumeDown) }},
		{"wake", func(g *rig) { g.r.Stop(alarms.StopWake) }},
		{"word", func(g *rig) { g.r.Stop(alarms.StopWord) }},
		{"timeout", func(g *rig) { g.step(start.Add(alarms.AlarmLimit)) }},
		{"shutdown", func(g *rig) { alarms.Shutdown(g.r) }},
	} {
		t.Run(c.reason, func(t *testing.T) {
			g := newRig(t, at(26, 6, 59, 0), 30)
			g.mustSetAlarm(g.book, at(26, 6, 59, 0), alarms.Alarm{ID: "wake", Hour: 7, Days: weekdays, Level: 80})
			g.step(start)
			g.sound("ring-1", start)
			g.step(start.Add(15 * time.Second))
			g.step(start.Add(alarms.RampDuration))
			g.want("the ramp", "end_session", "ringing true", "tone_start ring-1 ALARM", "transient 55", "transient 80")
			g.clock.set(start.Add(40 * time.Second))
			if c.reason == "timeout" {
				g.step(start.Add(alarms.AlarmLimit - time.Second))
				g.want("just before the limit")
			}
			c.stop(g)
			g.want("the stop", "tone_stop ring-1", "plain 30", "ringing false")
			g.wantEvents("ring_stop", "alarm wake "+c.reason)
			g.wantLevel(30)
			g.step(start.Add(alarms.AlarmLimit + time.Minute))
			g.want("after the stop")
		})
	}
}

// a timer rings at the level in force and never touches it, and gives up after a minute
func TestATimerNeverRampsAndTimesOutAfterAMinute(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	g.mustSetTimer(g.book, at(26, 6, 59, 0), "tea", 60)
	start := at(26, 7, 0, 0)
	g.step(start)
	g.want("the timer's end", "end_session", "ringing true", "tone_start ring-1 TIMER")
	g.sound("ring-1", start)
	if next := g.step(start.Add(10 * time.Second)); next > 50*time.Second {
		t.Errorf("next wake %v, past the timer's limit", next)
	}
	g.step(start.Add(alarms.TimerLimit - time.Millisecond))
	g.want("ringing")
	g.step(start.Add(alarms.TimerLimit))
	g.want("the limit", "tone_stop ring-1", "ringing false")
	g.wantEvents("ring_stop", "timer tea timeout")
}

// a shorter timer limit (the sim's, for the e2e: CADENCE_SIM_TIMER_SECONDS) gives a timer up after it instead
func TestATimerLimitEndsATimerSooner(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30, func(o *alarms.RingerOptions) { o.TimerLimit = 5 * time.Second })
	g.mustSetTimer(g.book, at(26, 6, 59, 0), "tea", 60)
	start := at(26, 7, 0, 0)
	g.step(start)
	g.want("the timer's end", "end_session", "ringing true", "tone_start ring-1 TIMER")
	if next := g.sound("ring-1", start); next > 5*time.Second {
		t.Errorf("next wake %v, past the timer's limit", next)
	}
	g.step(start.Add(5*time.Second - time.Millisecond))
	g.want("ringing")
	g.step(start.Add(5 * time.Second))
	g.want("the limit", "tone_stop ring-1", "ringing false")
	g.wantEvents("ring_stop", "timer tea timeout")
}

// no ramp from a level that is not the device's own, nor for an alarm set quieter than the level in force: then nothing
// is put back either
func TestNoRampFromAnUnknownLevelOrBelowTheLevelInForce(t *testing.T) {
	for _, c := range []struct {
		name         string
		level, alarm int
	}{{"unknown", -1, 80}, {"quieter", 60, 40}} {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, at(26, 6, 59, 0), c.level)
			g.mustSetAlarm(g.book, at(26, 6, 59, 0), alarms.Alarm{ID: "wake", Hour: 7, Days: weekdays, Level: c.alarm})
			start := at(26, 7, 0, 0)
			g.step(start)
			g.sound("ring-1", start)
			g.step(start.Add(15 * time.Second))
			g.step(start.Add(30 * time.Second))
			g.calls.take()
			g.r.StopRing(input.Action)
			g.want("the stop", "tone_stop ring-1", "ringing false")
		})
	}
}

// what comes due while one rings waits behind it and starts the moment it stops, ramped from the level put back; two
// alarms of one occurrence are one wake-up, two timers are not
func TestRingsWaitInTurnAndOneWakeUpMergesTwoAlarms(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	now := at(26, 6, 59, 0)
	g.mustSetAlarm(g.book, now, alarms.Alarm{ID: "weekdays", Hour: 7, Days: weekdays, Level: 80})
	g.mustSetAlarm(g.book, now, alarms.Alarm{ID: "mondays", Hour: 7, Days: mondays, Level: 40})
	g.mustSetTimer(g.book, now, "tea", 60)
	g.mustSetTimer(g.book, now, "egg", 60)
	start := at(26, 7, 0, 0)
	g.step(start)
	g.want("all due at once", "end_session", "ringing true", "tone_start ring-1 TIMER")
	g.sound("ring-1", start)

	g.clock.set(start.Add(5 * time.Second))
	g.r.StopRing(input.Action)
	// no session to end, the lights ring on
	g.want("the first stop", "tone_stop ring-1", "tone_start ring-2 TIMER")
	g.sound("ring-2", start.Add(5*time.Second))
	g.clock.set(start.Add(8 * time.Second))
	g.r.Stop(alarms.StopWord)
	g.want("the second stop", "tone_stop ring-2", "tone_start ring-3 ALARM")

	// the alarm's ramp is timed from its own start
	g.sound("ring-3", start.Add(8*time.Second))
	g.step(start.Add(11 * time.Second))
	g.want("3 s into the alarm", "transient 35")
	// a timer that ends while the alarm rings waits behind it, however long the alarm rings past its end
	g.mustSetTimer(g.book, start.Add(11*time.Second), "pasta", 5)
	g.step(start.Add(16 * time.Second))
	g.want("the timer's end under the alarm", "transient 40")

	g.clock.set(start.Add(20 * time.Second))
	if !g.r.Stop(alarms.StopWake) {
		t.Error("a stop with a ring waiting said nothing rings on")
	}
	g.want("the alarm's stop", "tone_stop ring-3", "plain 30", "tone_start ring-4 TIMER")
	g.clock.set(start.Add(21 * time.Second))
	if g.r.Stop(alarms.StopWake) {
		t.Error("the last stop said a ring rings on")
	}
	g.want("the last stop", "tone_stop ring-4", "ringing false")
	g.wantEvents("ring_start", "timer tea <nil>", "timer egg <nil>", "alarm weekdays <nil>", "timer pasta <nil>")
	if n := len(g.logs.events("ring_missed")); n != 0 {
		t.Errorf("%d ring_missed for a merged alarm", n)
	}
}

// while audiod cannot sound the ring, the lights ring alone and it is asked again about every second; once the ring is
// heard, its ramp catches up with the time since the ring began
func TestARingAudiodCannotSoundRingsLightsOnlyAndAsksAgain(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	g.mustSetAlarm(g.book, at(26, 6, 59, 0), alarms.Alarm{ID: "wake", Hour: 7, Days: weekdays, Level: 80})
	start := at(26, 7, 0, 0)
	g.speaker.fail(errDown)
	if next := g.step(start); next != time.Second {
		t.Errorf("next wake %v, want the retry", next)
	}
	g.want("audiod down", "end_session", "ringing true", "tone_start ring-1 ALARM")
	g.step(start.Add(500 * time.Millisecond))
	g.want("before the retry")
	g.step(start.Add(3 * time.Second))
	g.want("no ramp while unheard", "tone_start ring-1 ALARM")

	g.speaker.fail(nil)
	g.tone("ring-1", false, "gated")
	g.step(start.Add(3500 * time.Millisecond))
	g.want("gated")
	g.step(start.Add(4 * time.Second))
	g.want("asked again", "tone_start ring-1 ALARM")
	g.sound("ring-1", start.Add(9*time.Second))
	g.want("heard 9 s in", "transient 45")

	// audiod went away: the next one holds the level in force, so the ramp sends its level again once heard there
	g.tone("ring-1", false, "gone")
	g.step(start.Add(10 * time.Second))
	g.want("gone", "tone_start ring-1 ALARM")
	g.sound("ring-1", start.Add(10*time.Second))
	g.want("heard again", "transient 45")

	// an answer for another ring, or a stopped one, changes nothing
	g.tone("ring-9", false, "gone")
	g.tone("ring-1", false, "stopped")
	g.step(start.Add(10500 * time.Millisecond))
	g.want("stray answers")

	g.r.StopRing(input.Action)
	g.want("the stop", "tone_stop ring-1", "plain 30", "ringing false")
	g.wantEvents("ring_unheard", "alarm wake unavailable", "alarm wake gated", "alarm wake gone")
}

// a ring audiod refuses as invalid is never asked for again; it rings lights only to its limit
func TestAnInvalidRingIsNeverAskedAgain(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	g.mustSetTimer(g.book, at(26, 6, 59, 0), "tea", 60)
	start := at(26, 7, 0, 0)
	g.step(start)
	g.calls.take()
	g.tone("ring-1", false, "invalid")
	g.step(start.Add(2 * time.Second))
	g.step(start.Add(30 * time.Second))
	g.want("invalid")
	g.step(start.Add(alarms.TimerLimit))
	g.want("the limit", "tone_stop ring-1", "ringing false")
}

// at shutdown the ring that sounds stops and puts the level back; the waiting ones are missed
func TestShutdownStopsTheRingAndMissesTheWaiting(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	now := at(26, 6, 59, 0)
	g.mustSetAlarm(g.book, now, alarms.Alarm{ID: "wake", Hour: 7, Days: weekdays, Level: 80})
	g.mustSetTimer(g.book, now, "tea", 60)
	start := at(26, 7, 0, 0)
	g.step(start)
	g.sound("ring-1", start)
	g.step(start.Add(3 * time.Second))
	g.calls.take()
	alarms.Shutdown(g.r)
	g.want("shutdown", "tone_stop ring-1", "ringing false")
	g.wantEvents("ring_stop", "timer tea shutdown")
	g.wantEvents("ring_missed", "alarm wake <nil>")
	if g.r.Stop(alarms.StopButton) {
		t.Error("a ring after shutdown")
	}
}

// the loop wakes for a change of the book, and for a ring audiod answers
func TestTheRingLoopWakesOnABookChange(t *testing.T) {
	book, err := alarms.NewBook(alarms.StorePath(t.TempDir()), alarms.SystemClock{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	vol := volume.NewSetter(volume.NewStore(t.TempDir()), 30, slog.New(slog.DiscardHandler))
	c := &calls{}
	r := alarms.NewRinger(alarms.RingerOptions{Book: book, Volume: vol, Log: slog.New(slog.DiscardHandler)})
	r.UseSpeaker(&speaker{calls: c})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx, alarms.Hooks{})
	}()
	t.Cleanup(func() { cancel(); <-done })

	if _, err := book.SetTimer("tea", 1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !slices.Contains(c.take(), "tone_start ring-1 TIMER") {
		if time.Now().After(deadline) {
			t.Fatal("the timer never rang")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !r.Ringing() {
		t.Fatal("not ringing")
	}
	cancel()
	<-done
	if got := c.take(); !slices.Equal(got, []string{"tone_stop ring-1"}) {
		t.Fatalf("at shutdown %q", got)
	}
}

// held is a speaker whose StartTone waits until released, as a send to an audiod that stopped reading does
type held struct {
	speaker
	entered chan struct{}
	release chan struct{}
}

func (h *held) StartTone(id string, kind audiodv1.ToneKind) error {
	close(h.entered)
	<-h.release
	return h.speaker.StartTone(id, kind)
}

// whether a ring sounds is answered at once, whatever the speaker is doing: a press or a wake asks it
func TestRingingNeverWaitsForTheSpeaker(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	h := &held{speaker: speaker{calls: g.calls}, entered: make(chan struct{}), release: make(chan struct{})}
	g.r.UseSpeaker(h)
	g.mustSetTimer(g.book, at(26, 6, 59, 0), "tea", 60)
	stepped := make(chan struct{})
	go func() {
		defer close(stepped)
		g.step(at(26, 7, 0, 0))
	}()
	<-h.entered
	answered := make(chan bool, 1)
	go func() { answered <- g.r.Ringing() }()
	select {
	case ringing := <-answered:
		if !ringing {
			t.Error("not ringing while the tone starts")
		}
	case <-time.After(2 * time.Second):
		t.Error("Ringing waited for the speaker")
	}
	close(h.release)
	<-stepped
}

// once the speaker went away the hardware holds the level in force again: putting it back reports nothing found above
// it, and a raise after the ring is heard again is reported as it is
func TestAGoneRaiseIsNotReportedAtTheRestore(t *testing.T) {
	for _, c := range []struct {
		name       string
		heardAgain bool
		want       string
	}{{"stopped unheard", false, ""}, {"heard again", true, "found=45 level=30"}} {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, at(26, 6, 59, 0), 30)
			g.mustSetAlarm(g.book, at(26, 6, 59, 0), alarms.Alarm{ID: "wake", Hour: 7, Days: weekdays, Level: 80})
			start := at(26, 7, 0, 0)
			g.step(start)
			g.sound("ring-1", start.Add(9*time.Second))
			g.tone("ring-1", false, "gone")
			g.step(start.Add(10 * time.Second))
			if c.heardAgain {
				g.sound("ring-1", start.Add(10*time.Second))
			}
			g.r.StopRing(input.Action)
			got := ""
			g.volLogs.mu.Lock()
			logged := g.volLogs.b.String()
			g.volLogs.mu.Unlock()
			for line := range strings.Lines(logged) {
				if strings.Contains(line, "volume_reapplied") {
					got = line[strings.Index(line, "found="):]
					got = strings.TrimSpace(got)
				}
			}
			if got != c.want {
				t.Errorf("volume_reapplied %q, want %q", got, c.want)
			}
		})
	}
}
