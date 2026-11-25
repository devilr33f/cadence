package leds

import (
	"math"
	"reflect"
	"testing"
	"time"
)

var (
	round    = Geometry{Ring: 16, Button: 6, Digits: 4, Segments: 8, LightFull: 4}
	ringOnly = Geometry{Ring: 12}
	clockAt  = &ClockTime{Hour: 7, Minute: 42}
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func at(state State, since, clock int) Input {
	return Input{State: state, Since: ms(since), Clock: ms(clock), Time: clockAt, Brightness: 1}
}

func lit(g glyphs, v uint8) [][]uint8 {
	out := make([][]uint8, 4)
	for d := range out {
		out[d] = make([]uint8, 8)
		for s := 0; s < 7; s++ {
			if g[d]>>s&1 != 0 {
				out[d][s] = v
			}
		}
	}
	return out
}

func dark(px []RGB) bool {
	for _, p := range px {
		if p != (RGB{}) {
			return false
		}
	}
	return true
}

func peak(p RGB) int { return int(max(p.R, p.G, p.B)) }

func corrected(v uint8) float64 { return 255 * math.Pow(float64(v)/255, 2.2) }

// a non-zero led showing the base colour at some intensity, within rounding
func tinted(led, base RGB) bool {
	if led == (RGB{}) {
		return false
	}
	ch := func(c RGB) [3]float64 { return [3]float64{float64(c.R), float64(c.G), float64(c.B)} }
	b, l := ch(base), ch(led)
	top := 0
	for i := 1; i < 3; i++ {
		if b[i] > b[top] {
			top = i
		}
	}
	scale := l[top] / corrected(uint8(b[top]))
	for i := 0; i < 3; i++ {
		if math.Abs(l[i]-corrected(uint8(b[i]))*scale) > 1 {
			return false
		}
	}
	return true
}

func TestGlyphs(t *testing.T) {
	if got := timeGlyphs(7, 5); got != (glyphs{0x3F, 0x07, 0x3F, 0x6D}) {
		t.Fatalf("time = %v", got)
	}
	if got := numberGlyphs(35); got != (glyphs{0, 0, 0x4F, 0x6D}) {
		t.Fatalf("number = %v", got)
	}
	if numberGlyphs(100000) != numberGlyphs(9999) || numberGlyphs(-3) != numberGlyphs(0) {
		t.Fatal("number not clamped")
	}
	if got := countdownGlyphs(65); got != (glyphs{0, 0x06, 0x3F, 0x6D}) {
		t.Fatalf("65 s = %v", got)
	}
	if got := countdownGlyphs(3600); got != (glyphs{0, 0x06, 0x3F, 0x3F}) {
		t.Fatalf("1 h = %v", got)
	}
	if countdownGlyphs(1_000_000) != (glyphs{digitGlyph(9), digitGlyph(9), digitGlyph(5), digitGlyph(9)}) {
		t.Fatal("countdown not held at 99:59")
	}
}

func TestPainterGamma(t *testing.T) {
	// speaking at silence is 0.18 of full, which a linear led would show far too bright
	quiet := Render(round, at(Speaking, 0, 100))
	if b := quiet.Ring[0].B; b == 0 || b > 8 {
		t.Fatalf("quiet speaking ring blue = %d", b)
	}
	if got := newPainter(1).colour(RGB{0, 230, 170}, 1); got != (RGB{0, 203, 105}) {
		// 255*(230/255)^2.2 and 255*(170/255)^2.2
		t.Fatalf("colour = %v", got)
	}
	if newPainter(0.1).segment(1) != 20 || newPainter(1).segment(1) != 200 {
		t.Fatal("segments are linear: 200 * brightness")
	}
}

func TestBlend(t *testing.T) {
	from := newFrame(round)
	to := newFrame(round)
	to.Ring[3] = RGB{200, 100, 50}
	to.Button[0] = RGB{0, 0, 200}
	to.Digits[1][2] = 200
	to.Colon, to.Degree = 100, 80
	if !reflect.DeepEqual(Blend(from, to, 0), from) || !reflect.DeepEqual(Blend(from, to, 1), to) {
		t.Fatal("endpoints")
	}
	half := Blend(from, to, 0.5)
	if half.Ring[3] != (RGB{100, 50, 25}) || half.Button[0] != (RGB{0, 0, 100}) || half.Digits[1][2] != 100 ||
		half.Colon != 50 || half.Degree != 40 {
		t.Fatalf("half = %+v", half)
	}
}

func TestIdleShowsTime(t *testing.T) {
	f := Render(round, at(Idle, 0, 0))
	if !dark(f.Ring) || !dark(f.Button) || !reflect.DeepEqual(f.Digits, lit(timeGlyphs(7, 42), clockFull)) ||
		f.Colon != clockFull || f.Degree != 0 {
		t.Fatalf("idle = %+v", f)
	}
	in := at(Idle, 0, 0)
	in.Time = nil
	if f := Render(round, in); !reflect.DeepEqual(f.Digits, lit(dashGlyphs(), clockFull)) || f.Colon != 0 {
		t.Fatalf("no time must show dashes, got %+v", f)
	}
}

func TestOfflineBlinksTwiceSoftlyThenLooksIdle(t *testing.T) {
	for _, since := range []int{100, 500} {
		f := Render(round, at(Offline, since, since))
		for _, p := range f.Ring {
			if !tinted(p, offlineColour) {
				t.Fatalf("since %d ring = %v", since, p)
			}
		}
		if !tinted(f.Button[0], offlineColour) {
			t.Fatalf("since %d button = %v", since, f.Button[0])
		}
		if !reflect.DeepEqual(f.Digits, lit(timeGlyphs(7, 42), clockFull)) {
			t.Fatalf("clock stays on during the blink")
		}
	}
	rising, full := Render(round, at(Offline, 20, 20)), Render(round, at(Offline, 100, 100))
	if rising.Ring[0].R == 0 || rising.Ring[0].R >= full.Ring[0].R {
		t.Fatal("blink must ramp up")
	}
	if !dark(Render(round, at(Offline, 300, 300)).Ring) {
		t.Fatal("gap between blinks is dark")
	}
	if !reflect.DeepEqual(Render(round, at(Offline, 800, 800)), Render(round, at(Idle, 0, 0))) {
		t.Fatal("after the blinks offline looks idle")
	}
}

func TestListeningGlowsMintAndShowsTime(t *testing.T) {
	in := at(Listening, 0, 0)
	f := Render(round, in)
	// glow = 0.38 + 0.18*sin(0) + 0.35*level
	want := newPainter(1).colour(listeningColour, 0.38)
	for _, p := range f.Ring {
		if p != want {
			t.Fatalf("ring = %v want %v", p, want)
		}
	}
	if f.Button[5] != newPainter(1).colour(listeningColour, 0.6) {
		t.Fatalf("button = %v", f.Button[5])
	}
	if !reflect.DeepEqual(f.Digits, lit(timeGlyphs(7, 42), clockFull)) || f.Colon != clockFull {
		t.Fatal("listening shows the time")
	}
	in.Level = 1
	if Render(round, in).Ring[0] == want {
		t.Fatal("level must brighten the ring")
	}
}

func TestThinkingCometAndSnake(t *testing.T) {
	// head at 75 ms/step: at 0 the head sits on led 0, the tail trails behind it
	f := Render(round, at(Thinking, 0, 0))
	if peak(f.Ring[0]) == 0 || peak(f.Ring[15]) != 0 && peak(f.Ring[15]) >= peak(f.Ring[0]) {
		t.Fatalf("head not brightest: %v %v", f.Ring[0], f.Ring[15])
	}
	// led 0 is the head; the next led (1) is "ahead" and lit by the glide only when within one step
	lit0 := 0
	for _, p := range f.Ring {
		if p != (RGB{}) {
			lit0++
		}
	}
	if lit0 < 5 || lit0 > 8 {
		t.Fatalf("comet length %d", lit0)
	}
	if f.Button[0] != newPainter(1).colour(thinkingColour, 0.55) {
		t.Fatal("button")
	}
	// snake head on the first segment (a of digit 0) at full level, tail fading
	if f.Digits[0][0] != 200 {
		t.Fatalf("snake head = %d", f.Digits[0][0])
	}
	if f.Digits[0][5] == 0 || f.Digits[0][5] >= 200 {
		t.Fatalf("snake tail = %d", f.Digits[0][5])
	}
	if f.Colon != 0 {
		t.Fatal("no colon while thinking")
	}
	// comet glides: 37 ms later the head has moved about half a step
	g := Render(round, at(Thinking, 0, 37))
	if g.Ring[0] == f.Ring[0] {
		t.Fatal("comet must move between frames")
	}
}

func TestSpeakingFollowsLevel(t *testing.T) {
	quiet, loud := at(Speaking, 0, 0), at(Speaking, 0, 0)
	loud.Level = 1
	q, l := Render(round, quiet), Render(round, loud)
	if peak(l.Ring[0]) <= peak(q.Ring[0]) || peak(l.Button[0]) <= peak(q.Button[0]) {
		t.Fatal("louder reply must light more")
	}
	// loud: band = 1.3 + 0.35*sin(..) clamps near 1, so every digit shows its three rungs d, g, a
	for d := 0; d < 4; d++ {
		if l.Digits[d][3] != clockFull {
			t.Fatalf("digit %d bottom rung = %d", d, l.Digits[d][3])
		}
	}
	for d := range q.Digits {
		if q.Digits[d][0] != 0 {
			t.Fatal("silent reply lights no top rung")
		}
	}
}

func TestRingingPulsesAndBlinksTheTime(t *testing.T) {
	ring := func(since int) Frame { return Render(round, at(Ringing, since, since+4000)) }
	a0, a250, a500 := ring(0), ring(250), ring(500)
	for _, f := range []Frame{a0, a250, a500} {
		for _, p := range f.Ring {
			if !tinted(p, ringingColour) {
				t.Fatalf("ring = %v", p)
			}
		}
		if !dark(f.Button) {
			t.Fatal("button stays dark while ringing")
		}
	}
	if peak(a0.Ring[0]) <= 0 || peak(a0.Ring[0]) >= peak(a250.Ring[0]) || peak(a250.Ring[0]) >= peak(a500.Ring[0]) {
		t.Fatal("ring must pulse up over the first half second")
	}
	if a500.Ring[0] != newPainter(1).colour(ringingColour, 1) {
		t.Fatal("full colour at the peak")
	}
	if !reflect.DeepEqual(a0.Digits, lit(timeGlyphs(7, 42), clockFull)) || a0.Colon != clockFull {
		t.Fatal("time shows in the first half second")
	}
	for _, since := range []int{500, 750, 999} {
		f := ring(since)
		for _, row := range f.Digits {
			for _, v := range row {
				if v != 0 {
					t.Fatalf("clock must be dark at %d", since)
				}
			}
		}
	}
	if !reflect.DeepEqual(ring(1000), a0) || !reflect.DeepEqual(ring(2250), a250) {
		t.Fatal("period is one second")
	}
}

func TestMutedAndError(t *testing.T) {
	m := Render(round, at(Muted, 0, 0))
	if dark(m.Button) || peak(m.Ring[0]) == 0 || peak(m.Ring[0]) >= peak(m.Button[0]) {
		t.Fatalf("muted = dim ring, steady button: %+v", m)
	}
	if !reflect.DeepEqual(m.Digits, lit(timeGlyphs(7, 42), clockFull)) {
		t.Fatal("muted keeps the time")
	}
	e0, e500 := Render(round, at(Error, 0, 0)), Render(round, at(Error, 500, 500))
	if !tinted(e500.Ring[0], offlineColour) || peak(e0.Ring[0]) >= peak(e500.Ring[0]) {
		t.Fatal("error pulses the ring red")
	}
}

func TestDegreeDotAndCountdown(t *testing.T) {
	in := at(Idle, 0, 0)
	in.AlarmArmed = true
	if f := Render(round, in); f.Degree != clockFull || !reflect.DeepEqual(f.Digits, lit(timeGlyphs(7, 42), clockFull)) {
		t.Fatalf("armed idle = %+v", f)
	}
	in.Brightness = 0.1
	if f := Render(round, in); f.Degree != 20 || f.Degree != f.Colon {
		t.Fatal("dot dims with the room like the colon")
	}
	in.Time = nil
	in.Brightness = 1
	if Render(round, in).Degree != 0 {
		t.Fatal("no dot beside dashes")
	}
	secs := 90
	in.Countdown = &secs
	if f := Render(round, in); f.Degree != clockFull || !reflect.DeepEqual(f.Digits, lit(countdownGlyphs(90), clockFull)) {
		t.Fatal("countdown replaces dashes and takes the dot")
	}
	in.AlarmArmed = false
	in.Time = clockAt
	in.State = Offline
	in.Since = ms(800)
	if f := Render(round, in); !reflect.DeepEqual(f.Digits, lit(countdownGlyphs(90), clockFull)) || f.Colon != clockFull {
		t.Fatal("offline shows the countdown too")
	}
	in.State = Listening
	if f := Render(round, in); !reflect.DeepEqual(f.Digits, lit(timeGlyphs(7, 42), clockFull)) {
		t.Fatal("other states keep the time")
	}
	in.State = Ringing
	in.AlarmArmed = true
	if Render(round, in).Degree != 0 {
		t.Fatal("no dot while ringing")
	}
}

func TestVolumeOverlay(t *testing.T) {
	in := at(Listening, 0, 400)
	bare := Render(round, in)
	low := VolumeOverlay{Level: 20, Fill: 0.2, Opacity: 1}
	high := VolumeOverlay{Level: 80, Fill: 0.8, Opacity: 1}
	alone := func(o VolumeOverlay) Frame { c := in; c.Overlays = []VolumeOverlay{o}; return Render(round, c) }

	in.Overlays = []VolumeOverlay{low, high}
	if !reflect.DeepEqual(Render(round, in), alone(high)) {
		t.Fatal("a later full-opacity overlay covers earlier ones")
	}
	in.Overlays = []VolumeOverlay{high, {Level: 20, Fill: 0.2, Opacity: 0.5}}
	if !reflect.DeepEqual(Render(round, in), Blend(alone(high), alone(low), 0.5)) {
		t.Fatal("each overlay blends by its own opacity")
	}
	in.Overlays = []VolumeOverlay{{Level: 80, Fill: 0.8, Opacity: 0}}
	if !reflect.DeepEqual(Render(round, in), bare) {
		t.Fatal("opacity 0 is the bare state")
	}
	// 20 percent: 3.2 of 16 leds lit; the edge led is partly lit; the digits spell the level
	f := alone(low)
	if peak(f.Ring[2]) == 0 || peak(f.Ring[3]) == 0 || peak(f.Ring[3]) >= peak(f.Ring[2]) || peak(f.Ring[4]) != 0 {
		t.Fatalf("fill edge: %v %v %v", f.Ring[2], f.Ring[3], f.Ring[4])
	}
	if !reflect.DeepEqual(f.Digits, lit(numberGlyphs(20), clockFull)) {
		t.Fatal("level digits")
	}
}

func TestRingOnlyBoardHasNoClockParts(t *testing.T) {
	secs := 80
	for _, s := range []State{Idle, Listening, Thinking, Speaking, Offline, Ringing, Muted, Error} {
		in := at(s, 100, 100)
		in.AlarmArmed = true
		in.Countdown = &secs
		in.Overlays = []VolumeOverlay{{Level: 30, Fill: 0.3, Opacity: 1}}
		f := Render(ringOnly, in)
		if len(f.Ring) != 12 || len(f.Button) != 0 || len(f.Digits) != 0 || f.Colon != 0 || f.Degree != 0 {
			t.Fatalf("state %d: %+v", s, f)
		}
	}
}
