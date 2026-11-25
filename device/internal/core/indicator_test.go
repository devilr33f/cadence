package core

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
	"git.shino.boo/devilreef/cadence/device/internal/session"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

type fakeLights struct {
	states []leds.State
	shown  []int
	// the latest alarms and zone
	armed    bool
	timerEnd time.Time
	loc      *time.Location
}

func (f *fakeLights) SetState(s leds.State)               { f.states = append(f.states, s) }
func (f *fakeLights) ShowVolume(level int)                { f.shown = append(f.shown, level) }
func (f *fakeLights) SetAlarms(armed bool, end time.Time) { f.armed, f.timerEnd = armed, end }
func (f *fakeLights) SetLocation(loc *time.Location)      { f.loc = loc }

// a session shows its phase; at rest the lights show muted or idle; a refused activation blinks offline, but never
// over the muted look
func TestIndicatorPicksTheState(t *testing.T) {
	f := &fakeLights{}
	i := &indicator{lights: f}
	i.Phase(session.PhaseListening)
	i.Muted(true)
	i.Phase(session.PhaseIdle)
	i.Offline()
	i.Muted(false)
	i.Offline()
	i.Offline()
	i.Phase(session.PhaseThinking)
	i.Offline()
	i.Phase(session.PhaseSpeaking)
	i.Phase(session.PhaseIdle)
	i.ShowVolume(40)
	want := []leds.State{leds.Listening, leds.Muted, leds.Idle, leds.Offline, leds.Offline, leds.Thinking, leds.Speaking, leds.Idle}
	if !slices.Equal(f.states, want) || !slices.Equal(f.shown, []int{40}) {
		t.Fatalf("states %v, shown %v; want %v, [40]", f.states, f.shown, want)
	}
}

// a ring shows over whatever the session or the switch shows, and offline never blinks over it; the alarms and the
// zone go through to the lights
func TestIndicatorShowsARingOverAll(t *testing.T) {
	f := &fakeLights{}
	i := &indicator{lights: f}
	i.Phase(session.PhaseSpeaking)
	i.Ringing(true)
	i.Phase(session.PhaseIdle)
	i.Muted(true)
	i.Offline()
	i.Ringing(false)
	i.Muted(false)
	end := time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC)
	i.Alarms(true, end)
	zone := time.FixedZone("x", 6*3600)
	i.Zone(zone)
	want := []leds.State{leds.Speaking, leds.Ringing, leds.Muted, leds.Idle}
	if !slices.Equal(f.states, want) {
		t.Fatalf("states %v, want %v", f.states, want)
	}
	if !f.armed || !f.timerEnd.Equal(end) || f.loc != zone {
		t.Fatalf("alarms %t %v, zone %v", f.armed, f.timerEnd, f.loc)
	}
}

// the lights' dot, countdown and clock zone follow the book from the start
func TestTheLightsFollowTheBook(t *testing.T) {
	book, err := alarms.NewBook(alarms.StorePath(t.TempDir()), alarms.SystemClock{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	l := &syncLights{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		showAlarms(ctx, book, &indicator{lights: l})
	}()
	defer func() { cancel(); <-done }()

	l.wait(t, "nothing set, no zone", func(f *fakeLights) bool { return !f.armed && f.timerEnd.IsZero() && f.loc == nil })
	tm, err := book.SetTimer("tea", 60)
	if err != nil {
		t.Fatal(err)
	}
	l.wait(t, "the timer's end", func(f *fakeLights) bool { return f.timerEnd.Equal(tm.EndsAt) })
	if err := book.SetZone("Asia/Bishkek"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := book.SetAlarm(alarms.Alarm{ID: "wake", Hour: 7, Days: alarms.EveryDay, Level: 50}); err != nil {
		t.Fatal(err)
	}
	l.wait(t, "the dot and the zone", func(f *fakeLights) bool { return f.armed && f.loc != nil && f.loc.String() == "Asia/Bishkek" })
	book.CancelAllTimers()
	l.wait(t, "no countdown", func(f *fakeLights) bool { return f.timerEnd.IsZero() })
}

// syncLights is fakeLights for another goroutine
type syncLights struct {
	mu sync.Mutex
	f  fakeLights
}

func (l *syncLights) SetState(s leds.State) { l.do(func(f *fakeLights) { f.SetState(s) }) }
func (l *syncLights) ShowVolume(level int)  { l.do(func(f *fakeLights) { f.ShowVolume(level) }) }
func (l *syncLights) SetAlarms(armed bool, end time.Time) {
	l.do(func(f *fakeLights) { f.SetAlarms(armed, end) })
}
func (l *syncLights) SetLocation(loc *time.Location) {
	l.do(func(f *fakeLights) { f.SetLocation(loc) })
}

func (l *syncLights) do(f func(*fakeLights)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(&l.f)
}

func (l *syncLights) wait(t *testing.T, what string, cond func(*fakeLights) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		l.mu.Lock()
		ok := cond(&l.f)
		l.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lights: no %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// a sender's or an api caller's level shows on the lights as a press's does; a restore, a press and a word do not
// show through the listener (the press and the word show their own)
func TestOutsideVolumeShows(t *testing.T) {
	f := &fakeLights{}
	vol := volume.NewSetter(volume.NewStore(t.TempDir()), 30, nil)
	showOutsideVolume(vol, &indicator{lights: f})
	for _, step := range []struct {
		level  int
		origin volume.Origin
	}{{40, volume.OriginSender}, {35, volume.OriginButton}, {30, volume.OriginWord}, {50, volume.OriginCommand}} {
		if _, err := vol.Set(step.level, step.origin); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(f.shown, []int{40, 50}) {
		t.Fatalf("shown %v, want [40 50]", f.shown)
	}
}

// an error pulses red at rest until its time is up, and a session that begins shows over it
func TestIndicatorPulsesAnError(t *testing.T) {
	f := &fakeLights{}
	var ends []func()
	i := &indicator{lights: f, afterFunc: func(_ time.Duration, fn func()) { ends = append(ends, fn) }}
	i.Error()
	ends[0]()
	i.Error()
	i.Phase(session.PhaseListening)
	ends[1]()
	i.Phase(session.PhaseIdle)
	i.Muted(true)
	i.Error()
	i.Error()
	ends[2]()
	ends[3]()
	want := []leds.State{leds.Error, leds.Idle, leds.Error, leds.Listening, leds.Idle, leds.Muted, leds.Error, leds.Muted}
	if !slices.Equal(f.states, want) {
		t.Fatalf("states %v, want %v", f.states, want)
	}
}

// a heard word pulses once at rest, over the clock, the muted look and an error alike; a ring or a
// session that runs shows over it, and the word while one runs leaves no pulse
func TestIndicatorAcknowledgesAHeardWord(t *testing.T) {
	f := &fakeLights{}
	var ends []func()
	i := &indicator{lights: f, afterFunc: func(_ time.Duration, fn func()) { ends = append(ends, fn) }}
	i.Heard()
	ends[0]()
	i.Muted(true)
	i.Heard()
	ends[1]()
	i.Ringing(true)
	i.Heard()
	i.Ringing(false)
	i.Phase(session.PhaseListening)
	i.Heard()
	i.Phase(session.PhaseIdle)
	want := []leds.State{leds.Heard, leds.Idle, leds.Muted, leds.Heard, leds.Muted, leds.Ringing, leds.Muted,
		leds.Listening, leds.Muted}
	if !slices.Equal(f.states, want) {
		t.Fatalf("states %v, want %v", f.states, want)
	}
}
