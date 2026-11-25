package leds

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"
)

const (
	frameInterval = 33 * time.Millisecond
	crossfade     = 200 * time.Millisecond

	volumeShown   = 2 * time.Second
	volumeFadeIn  = 150 * time.Millisecond
	volumeFadeOut = 250 * time.Millisecond
	volumeGlide   = 60 * time.Millisecond

	levelAttack    = 60 * time.Millisecond
	levelRelease   = 250 * time.Millisecond
	brightnessSet  = 2 * time.Second
	lightInterval  = 500 * time.Millisecond
	noSensor       = 0.6
	dimmestRoom    = 0.1
	firstSaneYear  = 2026
	maxCountdownS  = math.MaxInt32
	sinkFailedCode = "sink_failed"
)

var ErrGeometry = errors.New("leds: impossible geometry")

type Options struct {
	Sink Sink
	// defaults to slog.Default
	Log *slog.Logger
	// the clock for every animation, the time on the display and timer countdowns; defaults to time.Now
	Now func() time.Time
	// Tick paces the loop; stop ends it. defaults to a time.Ticker
	Tick func(d time.Duration) (ticks <-chan time.Time, stop func())
	// the zone the display shows until SetLocation; defaults to UTC
	Location *time.Location
}

// Renderer draws the lights at about 30 fps on its own goroutine, crossfading from what shows on every state change.
// The setters only store and never wait on the sink; a sink failure blanks the lights once, logs once and ends the
// drawing for good, so lights never hold up audio.
type Renderer struct {
	sink   Sink
	geo    Geometry
	sensor LightSensor
	log    *slog.Logger
	now    func() time.Time
	tick   func(time.Duration) (<-chan time.Time, func())
	loc    *time.Location

	mu      sync.Mutex
	started time.Time

	state        State
	stateSince   time.Time
	stateChanged bool
	micPeak      float64
	replyPeak    float64
	alarmArmed   bool
	timerEnd     time.Time
	lights       bool
	clock        bool

	volume     *int
	volShownAt time.Time
	volUntil   time.Time
	volFill    *float64

	music musicMeter

	// frame to frame
	lastStep    *time.Time
	level       float64
	lastFrame   *Frame
	fadeFrom    *Frame
	fadeStart   time.Time
	brightness  *float64
	lightTarget float64
	lightDue    time.Time // only the stepping goroutine touches it
	done        bool
	failed      bool
	drew        bool

	run    sync.Mutex // guards cancel and wg across Start and Stop
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(o Options) (*Renderer, error) {
	if o.Sink == nil {
		return nil, errors.New("leds: no sink")
	}
	geo := o.Sink.Geometry()
	if !geo.valid() {
		return nil, ErrGeometry
	}
	r := &Renderer{sink: o.Sink, geo: geo, log: o.Log, now: o.Now, tick: o.Tick, loc: o.Location,
		lights: true, clock: true, lightTarget: noSensor}
	if r.log == nil {
		r.log = slog.Default()
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.tick == nil {
		r.tick = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	if r.loc == nil {
		r.loc = time.UTC
	}
	r.sensor, _ = o.Sink.(LightSensor)
	r.started = r.now()
	r.stateSince = r.started
	return r, nil
}

// SetState shows a state. A repeated offline blinks again; any other repeat keeps its animation running.
func (r *Renderer) SetState(s State) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if s == r.state && s != Offline {
		return
	}
	r.state, r.stateSince, r.stateChanged = s, now, true
	// audio offered before the change belongs to the state that is over
	r.micPeak, r.replyPeak = 0, 0
}

// ShowVolume fades the level in over any state, holds it 2 s after the latest call, then fades it out.
func (r *Renderer) ShowVolume(level int) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	// a press while the overlay fades picks up from what shows instead of flickering
	shown := 0.0
	if r.volume != nil {
		shown = r.volumeOpacity(now)
	}
	r.volShownAt = now.Add(-time.Duration(float64(volumeFadeIn) * shown))
	r.volume = &level
	r.volUntil = now.Add(volumeShown)
}

// SetSwitches are the owner's switches, on until set: off blanks ring and button, or the clock segments, from the
// next frame on with no fade, the volume overlay included; on draws the current state again.
func (r *Renderer) SetSwitches(lights, clock bool) {
	r.mu.Lock()
	r.lights, r.clock = lights, clock
	r.mu.Unlock()
}

// SetMicLevel offers a 0..1 peak of the microphone; only listening shows it. The microphone stays open during
// playback for barge-in, so speaking must never follow it. The loudest offer since the last frame counts.
func (r *Renderer) SetMicLevel(v float64) {
	r.mu.Lock()
	r.micPeak = max(r.micPeak, clamp01(v))
	r.mu.Unlock()
}

// SetReplyLevel offers a 0..1 peak of the reply audio; only speaking shows it.
func (r *Renderer) SetReplyLevel(v float64) {
	r.mu.Lock()
	r.replyPeak = max(r.replyPeak, clamp01(v))
	r.mu.Unlock()
}

// SetMusic offers the latest spectrum bands of the music playing, 0..255 each over -60..0 dBFS, and how to show them;
// idle draws them until they stop coming.
func (r *Renderer) SetMusic(bands [MusicBandCount]uint8, style MusicStyle) {
	now := r.now()
	r.mu.Lock()
	r.music.offer(bands, style, now)
	r.mu.Unlock()
}

// SetAlarms says whether any alarm is set and when the nearest timer ends (zero: none).
func (r *Renderer) SetAlarms(armed bool, nearestTimerEnd time.Time) {
	r.mu.Lock()
	r.alarmArmed, r.timerEnd = armed, nearestTimerEnd
	r.mu.Unlock()
}

// SetLocation is the zone the display shows from the next frame on; nil keeps the one it shows.
func (r *Renderer) SetLocation(loc *time.Location) {
	if loc == nil {
		return
	}
	r.mu.Lock()
	r.loc = loc
	r.mu.Unlock()
}

// Start begins drawing on a goroutine of its own; a board with no lights draws nothing.
func (r *Renderer) Start(ctx context.Context) {
	r.run.Lock()
	defer r.run.Unlock()
	if r.cancel != nil || r.geo.empty() {
		return
	}
	ctx, r.cancel = context.WithCancel(ctx)
	ticks, stop := r.tick(frameInterval)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				if !r.step(r.now()) {
					return
				}
			}
		}
	}()
}

// Stop ends the drawing, then blanks the lights unless they failed or were never drawn: a frozen frame would keep
// showing a state that is over.
func (r *Renderer) Stop() {
	r.run.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.run.Unlock()
	r.wg.Wait()
	r.mu.Lock()
	blank := !r.done && r.drew
	r.done = true
	r.mu.Unlock()
	if blank {
		_ = r.write(newFrame(r.geo))
	}
}

func approach(current, target float64, elapsed, tau time.Duration) float64 {
	return target + (current-target)*math.Exp(-float64(elapsed)/float64(tau))
}

// how far part is through whole, 0..1
func fraction(part, whole time.Duration) float64 { return clamp01(float64(part) / float64(whole)) }

func roomBrightness(raw, full int) float64 {
	lit := math.Log10(1+float64(max(raw, 0))) / math.Log10(1+float64(full))
	return dimmestRoom + (1-dimmestRoom)*clamp01(lit)
}

// how much of the volume overlay shows; needs mu
func (r *Renderer) volumeOpacity(now time.Time) float64 {
	return math.Min(fraction(now.Sub(r.volShownAt), volumeFadeIn), 1-fraction(now.Sub(r.volUntil), volumeFadeOut))
}

func (r *Renderer) localTime(now time.Time) *ClockTime {
	t := now.In(r.loc)
	if t.Year() < firstSaneYear {
		return nil
	}
	return &ClockTime{Hour: t.Hour(), Minute: t.Minute()}
}

func (r *Renderer) sample(now time.Time) {
	if r.sensor == nil || !r.lightDue.IsZero() && now.Before(r.lightDue) {
		return
	}
	r.lightDue = now.Add(lightInterval)
	target := noSensor
	if raw, ok := r.sensor.Light(); ok && r.geo.LightFull > 0 {
		target = roomBrightness(raw, r.geo.LightFull)
	}
	r.mu.Lock()
	r.lightTarget = target
	r.mu.Unlock()
}

// step draws one frame at now; false once failed or stopped. only one goroutine steps.
func (r *Renderer) step(now time.Time) bool {
	r.sample(now)
	frame, ok := r.compose(now)
	if !ok {
		return false
	}
	if r.geo.empty() {
		return true
	}
	if err := r.write(frame); err != nil {
		// the blank may fail as well; there is nothing more to try for lights
		_ = r.write(newFrame(r.geo))
		code := sinkFailedCode
		var se *SinkError
		if errors.As(err, &se) {
			code = se.Reason
		}
		r.log.Warn("leds_failed", "reason", code)
		r.mu.Lock()
		r.done, r.failed = true, true
		r.mu.Unlock()
		return false
	}
	r.mu.Lock()
	r.drew = true
	r.mu.Unlock()
	return true
}

// compose builds the frame to write: the state, the overlay and the fade, masked by the owner's switches
func (r *Renderer) compose(now time.Time) (Frame, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return Frame{}, false
	}
	var elapsed time.Duration
	if r.lastStep != nil {
		// a clock that stepped back must not run the filters backwards
		elapsed = max(now.Sub(*r.lastStep), 0)
	}
	r.lastStep = &now

	if r.volume != nil && !now.Before(r.volUntil.Add(volumeFadeOut)) {
		r.volume, r.volFill = nil, nil
	}
	in := Input{State: r.state, Since: max(now.Sub(r.stateSince), 0), Clock: max(now.Sub(r.started), 0), Time: r.localTime(now),
		AlarmArmed: r.alarmArmed}
	if !r.timerEnd.IsZero() {
		left := int(min(max(math.Ceil(r.timerEnd.Sub(now).Seconds()), 0), maxCountdownS))
		in.Countdown = &left
	}

	// the fade starts from what shows, even part way through another fade, so nothing ever jumps
	if r.stateChanged && r.lastFrame != nil {
		r.fadeFrom, r.fadeStart = r.lastFrame, r.stateSince
	}
	r.stateChanged = false

	// taken every frame, so audio from an earlier state never shows in a later one
	target := 0.0
	switch r.state {
	case Listening:
		target = r.micPeak
	case Speaking:
		target = r.replyPeak
	}
	r.micPeak, r.replyPeak = 0, 0
	tau := levelRelease
	if target > r.level {
		tau = levelAttack
	}
	r.level = approach(r.level, target, elapsed, tau)
	in.Level = r.level
	in.Music = r.music.step(now, elapsed)

	if r.brightness == nil {
		b := r.lightTarget
		r.brightness = &b
	} else {
		*r.brightness = approach(*r.brightness, r.lightTarget, elapsed, brightnessSet)
	}
	in.Brightness = *r.brightness

	if r.volume != nil {
		// a new overlay starts at its level; a press while it shows glides there
		fill := clamp01(float64(*r.volume) / 100)
		if r.volFill == nil {
			r.volFill = &fill
		} else {
			*r.volFill = approach(*r.volFill, fill, elapsed, volumeGlide)
		}
		in.Overlays = []VolumeOverlay{{Level: *r.volume, Fill: *r.volFill, Opacity: r.volumeOpacity(now)}}
	}

	frame := Render(r.geo, in)
	if r.fadeFrom != nil {
		if faded := fraction(now.Sub(r.fadeStart), crossfade); faded < 1 {
			frame = Blend(*r.fadeFrom, frame, faded)
		} else {
			r.fadeFrom = nil
		}
	}
	r.lastFrame = &frame
	// the switches mask only what is written, so a light turned back on shows the state drawn all along
	return mask(r.geo, frame, r.lights, r.clock), true
}

func mask(g Geometry, f Frame, lights, clock bool) Frame {
	out := newFrame(g)
	if lights {
		copy(out.Ring, f.Ring)
		copy(out.Button, f.Button)
	}
	if clock {
		for d := range f.Digits {
			copy(out.Digits[d], f.Digits[d])
		}
		out.Colon, out.Degree = f.Colon, f.Degree
	}
	return out
}

// write sends a frame to every part the board has; the first error ends it
func (r *Renderer) write(f Frame) error {
	if r.geo.Ring > 0 {
		if err := r.sink.Ring(f.Ring); err != nil {
			return err
		}
	}
	if r.geo.Button > 0 {
		if err := r.sink.Button(f.Button); err != nil {
			return err
		}
	}
	if r.geo.Digits > 0 {
		level := make(map[Segment]uint8, r.geo.Digits*7+2)
		for d, row := range f.Digits {
			for s := 0; s < 7; s++ {
				level[Segment{d, s}] = row[s]
			}
		}
		level[Colon], level[Degree] = f.Colon, f.Degree
		return r.sink.Segments(level)
	}
	return nil
}
