package leds

import (
	"math"
	"time"
)

type State uint8

const (
	Idle State = iota
	Listening
	Thinking
	Speaking
	Offline
	Ringing
	// muted and Error are v2 additions: a dim steady ring with a lit button, and a red pulse
	Muted
	Error
	// Heard is the command spotter's acknowledgment: one cyan flash fading out
	Heard
)

type ClockTime struct{ Hour, Minute int }

// Input is everything a frame depends on.
type Input struct {
	State State
	// time in the state
	Since time.Duration
	// monotonic time for free-running animations
	Clock time.Duration
	// smoothed 0..1 of the state's audio
	Level float64
	// nil: not yet known
	Time *ClockTime
	// 0.1..1, applied to every channel
	Brightness float64
	// an alarm is set
	AlarmArmed bool
	// seconds until the nearest timer ends; idle and offline show it instead of the time
	Countdown *int
	// music playing while idle; nil for none
	Music *Music
	// drawn over the state in order, the last on top
	Overlays []VolumeOverlay
}

const (
	snakeStepMs = 70.0
	snakeTail   = 3.0

	cometStepMs  = 75.0
	cometTail    = 6.0
	cometFalloff = 1.6

	blinkMs       = 250.0
	secondBlinkMs = 450.0
	blinkRampMs   = 60.0
	offlineGlow   = 0.8

	ringPeriodMs = 1000
	ringDimmest  = 0.2

	mutedRing   = 0.12
	mutedButton = 0.5
)

type segmentAt struct{ digit, segment int }

// the outer segments of the four digits, clockwise from the top of the first
var snake = [...]segmentAt{{0, 0}, {1, 0}, {2, 0}, {3, 0}, {3, 1}, {3, 2}, {3, 3}, {2, 3}, {1, 3}, {0, 3}, {0, 4}, {0, 5}}

func millis(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Render draws one frame: the same input always draws the same frame. Switches and fades are the renderer's.
func Render(g Geometry, in Input) Frame {
	paint := newPainter(in.Brightness)
	f := newFrame(g)
	clockMs := millis(in.Clock)
	switch in.State {
	case Idle:
		showClock(&f, in, paint)
		drawMusic(&f, in.Music, in.Countdown != nil, paint)
	case Listening:
		listening(&f, in, clockMs, paint)
	case Thinking:
		thinking(&f, clockMs, paint)
	case Speaking:
		speaking(&f, in, clockMs, paint)
	case Offline:
		offline(&f, in, paint)
	case Ringing:
		ringing(&f, in, paint)
	case Muted:
		fill(f.Ring, paint.colour(offlineColour, mutedRing))
		fill(f.Button, paint.colour(offlineColour, mutedButton))
		showTime(&f, in.Time, paint)
	case Error:
		fill(f.Ring, paint.colour(offlineColour, ringIntensity(in.Since)))
		showTime(&f, in.Time, paint)
	case Heard:
		heard(&f, in, paint)
	}
	return foldOverlays(f, in.Overlays, in.Brightness)
}

// heardFlash is how long the acknowledgment's flash takes to fade out
const heardFlashMs = 600.0

// heard is the command spotter's acknowledgment: the listening colour flashes once and fades,
// so the word is seen taken even with nothing else playing
func heard(f *Frame, in Input, paint painter) {
	glow := 1 - clamp01(millis(in.Since)/heardFlashMs)
	if glow <= 0 {
		return
	}
	fill(f.Ring, paint.colour(listeningColour, glow))
	fill(f.Button, paint.colour(listeningColour, 0.6*glow))
	showTime(f, in.Time, paint)
}

func fill(px []RGB, c RGB) {
	for i := range px {
		px[i] = c
	}
}

func showTime(f *Frame, t *ClockTime, paint painter) {
	if len(f.Digits) != 4 {
		return
	}
	if t == nil {
		dashGlyphs().show(f, paint.segment(1))
		return
	}
	timeGlyphs(t.Hour, t.Minute).show(f, paint.segment(1))
	f.Colon = paint.segment(1)
}

// the clock of idle and offline: a timer's countdown if one runs, else the time. the dot says an alarm is set, so it
// never stands beside dashes
func showClock(f *Frame, in Input, paint painter) {
	if len(f.Digits) != 4 {
		return
	}
	if in.Countdown != nil {
		countdownGlyphs(*in.Countdown).show(f, paint.segment(1))
		f.Colon = paint.segment(1)
	} else {
		showTime(f, in.Time, paint)
	}
	if in.AlarmArmed && (in.Countdown != nil || in.Time != nil) {
		f.Degree = paint.segment(1)
	}
}

func listening(f *Frame, in Input, clockMs float64, paint painter) {
	glow := 0.38 + 0.18*math.Sin(clockMs/260) + 0.35*in.Level
	fill(f.Ring, paint.colour(listeningColour, glow))
	fill(f.Button, paint.colour(listeningColour, 0.6))
	showTime(f, in.Time, paint)
}

func thinking(f *Frame, clockMs float64, paint painter) {
	n := float64(len(f.Ring))
	if n > 0 {
		head := math.Mod(clockMs/cometStepMs, n)
		for i := range f.Ring {
			behind := math.Mod(head-float64(i)+n, n)
			// the led the head moves into fades in, so the comet glides instead of stepping
			ahead := n - behind
			if behind < cometTail {
				f.Ring[i] = paint.colour(thinkingColour, math.Pow(1-behind/cometTail, cometFalloff))
			} else if ahead < 1 {
				f.Ring[i] = paint.colour(thinkingColour, 1-ahead)
			}
		}
	}
	fill(f.Button, paint.colour(thinkingColour, 0.55))

	if len(f.Digits) == 4 {
		steps := float64(len(snake))
		step := math.Mod(clockMs/snakeStepMs, steps)
		for i, s := range snake {
			behind := math.Mod(step-float64(i)+steps, steps)
			if behind < snakeTail {
				f.Digits[s.digit][s.segment] = paint.segment(1 - behind/snakeTail)
			}
		}
	}
}

func speaking(f *Frame, in Input, clockMs float64, paint painter) {
	fill(f.Ring, paint.colour(speakingColour, 0.18+0.75*in.Level))
	fill(f.Button, paint.colour(speakingColour, 0.3+0.5*in.Level))
	// a four-band meter: each digit wobbles at its own rate around the reply's level
	rungs := [3]int{3, 6, 0}
	for d := range f.Digits {
		off := float64(d)
		band := in.Level*1.3 + 0.35*math.Sin(clockMs/(60+17*off)+off)
		lit := int(math.Round(3 * clamp01(band)))
		for r := 0; r < lit; r++ {
			f.Digits[d][rungs[r]] = paint.segment(1)
		}
	}
}

func ringing(f *Frame, in Input, paint painter) {
	fill(f.Ring, paint.colour(ringingColour, ringIntensity(in.Since)))
	if in.Since.Milliseconds()%ringPeriodMs < ringPeriodMs/2 {
		showTime(f, in.Time, paint)
	}
}

// the ring pulses once a second between 0.2 and 1 intensity, dimmest at the start
func ringIntensity(since time.Duration) float64 {
	phase := float64(since.Milliseconds()%ringPeriodMs) / ringPeriodMs
	return ringDimmest + (1-ringDimmest)*(0.5-0.5*math.Cos(2*math.Pi*phase))
}

// how much of a blink shows at x ms into it: it ramps up and down instead of switching
func blink(x float64) float64 {
	if x < 0 || x >= blinkMs {
		return 0
	}
	return clamp01(math.Min(x, blinkMs-x) / blinkRampMs)
}

func offlineBlink(since time.Duration) float64 {
	s := millis(since)
	return math.Max(blink(s), blink(s-secondBlinkMs))
}

func offline(f *Frame, in Input, paint painter) {
	if shown := offlineBlink(in.Since); shown > 0 {
		fill(f.Ring, paint.colour(offlineColour, offlineGlow*shown))
		fill(f.Button, paint.colour(offlineColour, offlineGlow*shown))
	}
	showClock(f, in, paint)
}
