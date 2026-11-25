package core

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	"git.shino.boo/devilreef/cadence/device/internal/session"
)

// lights draws on a board's lights: the renderer, the owner's switches as a capability, and the indicator that
// picks the state
type lights struct {
	r   *leds.Renderer
	cap capability.Provider
	ind *indicator
}

// startLights draws on the board's lights from now until stop; nil when it has none.
func startLights(ctx context.Context, b board.Board, dataDir string, log *slog.Logger) *lights {
	lb, ok := b.(board.Lights)
	if !ok {
		return nil
	}
	sink, ok := lb.Lights()
	if !ok {
		return nil
	}
	r, err := leds.New(leds.Options{Sink: sink, Log: log})
	if err != nil {
		reason := "sink"
		if errors.Is(err, leds.ErrGeometry) {
			reason = "geometry"
		}
		log.Error("leds_refused", "reason", reason)
		return nil
	}
	l := &lights{r: r, cap: leds.NewCapability(r, filepath.Join(dataDir, "state", "lights"), log), ind: &indicator{lights: r}}
	r.Start(ctx)
	if vb, ok := b.(board.Visualizer); ok {
		if frames := vb.MediaViz(); frames != nil {
			go showMusic(ctx, frames, r)
		}
	}
	return l
}

var musicStyles = map[string]leds.MusicStyle{
	"bands": leds.MusicBands, "pulse": leds.MusicPulse, "spectrum": leds.MusicSpectrum, "equalizer": leds.MusicEqualizer,
	"orbit": leds.MusicOrbit, "beat": leds.MusicBeat,
}

// showMusic hands the music's spectrum to the lights until ctx is done
func showMusic(ctx context.Context, frames <-chan media.VizFrame, r interface {
	SetMusic([leds.MusicBandCount]uint8, leds.MusicStyle)
}) {
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-frames:
			r.SetMusic(f.Bands, musicStyles[f.Style])
		}
	}
}

// stop blanks the lights.
func (l *lights) stop() {
	if l != nil {
		l.r.Stop()
	}
}

func (l *lights) capabilities() []capability.Provider {
	if l == nil {
		return nil
	}
	return []capability.Provider{l.cap}
}

// lightsPort is what the indicator drives: the renderer
type lightsPort interface {
	SetState(leds.State)
	ShowVolume(level int)
	SetAlarms(armed bool, nearestTimerEnd time.Time)
	SetLocation(loc *time.Location)
}

// indicator shows a ring over all else, the session's phase while one runs, else muted or idle; an activation refused
// for want of a link blinks offline. It is the input's Lights.
type indicator struct {
	lights lightsPort

	// afterFunc runs f after d, nil for time.AfterFunc: the end of an error's pulse
	afterFunc func(d time.Duration, f func())

	mu      sync.Mutex
	phase   session.Phase
	muted   bool
	ringing bool
	shown   leds.State
	// the heard and the error pulse show at rest until then
	heardSeq  uint64
	heardShow bool
	errorSeq  uint64
	errorShow bool
}

// errorPulse is how long a failure's red pulse shows
const errorPulse = 1500 * time.Millisecond

func (i *indicator) Phase(p session.Phase) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.phase = p
	// a session that begins ends the pulse of the one before
	if p != session.PhaseIdle {
		i.errorShow = false
	}
	i.show()
}

func (i *indicator) Muted(muted bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.muted = muted
	i.show()
}

func (i *indicator) ShowVolume(level int) { i.lights.ShowVolume(level) }

// Ringing shows an alarm's or a timer's ring until it stops.
func (i *indicator) Ringing(on bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ringing = on
	i.show()
}

// Alarms are the clock's dot while any alarm is set and the nearest timer's countdown (zero: none).
func (i *indicator) Alarms(armed bool, nearestTimerEnd time.Time) {
	i.lights.SetAlarms(armed, nearestTimerEnd)
}

// Zone is the device's own zone, which its clock shows from now on.
func (i *indicator) Zone(loc *time.Location) { i.lights.SetLocation(loc) }

// Offline blinks at rest; the muted look stays, as the switch, not the link, refused the press, and so does a ring
func (i *indicator) Offline() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.phase == session.PhaseIdle && !i.muted && !i.ringing {
		i.set(leds.Offline)
	}
}

// Error pulses red at rest for errorPulse: a request failed. A ring or a session that begins shows over it
func (i *indicator) Error() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.errorSeq++
	seq := i.errorSeq
	i.errorShow = true
	i.show()
	after := i.afterFunc
	if after == nil {
		after = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	after(errorPulse, func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		if i.errorSeq == seq {
			i.errorShow = false
			i.show()
		}
	})
}

// callers hold mu
func (i *indicator) show() {
	if i.ringing {
		i.set(leds.Ringing)
		return
	}
	s := leds.Idle
	switch i.phase {
	case session.PhaseListening:
		s = leds.Listening
	case session.PhaseThinking:
		s = leds.Thinking
	case session.PhaseSpeaking:
		s = leds.Speaking
	default:
		switch {
		case i.heardShow:
			s = leds.Heard
		case i.errorShow:
			s = leds.Error
		case i.muted:
			s = leds.Muted
		}
	}
	i.set(s)
}

// heardPulse is how long a heard word's acknowledgment shows
const heardPulse = 700 * time.Millisecond

// Heard acknowledges a command word the spotter heard: a brief pulse at rest,
// over the clock and the music. A session's own phase and a ring show over it.
func (i *indicator) Heard() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.phase != session.PhaseIdle || i.ringing {
		return
	}
	i.heardSeq++
	seq := i.heardSeq
	i.heardShow = true
	i.show()
	after := i.afterFunc
	if after == nil {
		after = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	after(heardPulse, func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		if i.heardSeq == seq {
			i.heardShow = false
			i.show()
		}
	})
}

// set passes a change on, and offline every time: it blinks again. callers hold mu
func (i *indicator) set(s leds.State) {
	if s == i.shown && s != leds.Offline {
		return
	}
	i.shown = s
	i.lights.SetState(s)
}
