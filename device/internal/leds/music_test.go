package leds

import (
	"testing"
	"time"
)

func loud(v uint8) (b [MusicBandCount]uint8) {
	for i := range b {
		b[i] = v
	}
	return b
}

// music shows on the resting ring while frames come, and fades out once they stop
func TestMusicShowsWhileIdleAndFadesWhenItStops(t *testing.T) {
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	r.step(clk.now)
	if ringPeak(sink.last()) != 0 {
		t.Fatal("idle ring lit before any music")
	}
	for range 10 {
		r.SetMusic(loud(230), MusicBands)
		clk.advance(33 * time.Millisecond)
		r.step(clk.now)
	}
	if ringPeak(sink.last()) == 0 {
		t.Fatal("music never showed")
	}
	clk.advance(2 * time.Second)
	r.step(clk.now)
	clk.advance(33 * time.Millisecond)
	r.step(clk.now)
	if ringPeak(sink.last()) != 0 {
		t.Fatalf("music still shows after it stopped: %d", ringPeak(sink.last()))
	}
}

// a session's state owns the ring: music never draws over listening
func TestMusicStaysOffTheSessionStates(t *testing.T) {
	m := &Music{Style: MusicBands, Groups: [3]float64{1, 1, 1}, Level: 1}
	in := at(Listening, 1000, 1000)
	without := Render(round, in)
	in.Music = m
	with := Render(round, in)
	for i := range with.Ring {
		if with.Ring[i] != without.Ring[i] {
			t.Fatal("music drew over listening")
		}
	}
}

// each style draws: bands fill each third by its group, spectrum lights every led in its band's hue, pulse fills the
// ring, equalizer takes the clock's digits unless a countdown runs
func TestMusicStyles(t *testing.T) {
	half := &Music{Style: MusicBands, Groups: [3]float64{1, 0, 0.5}}
	f := Render(ringOnly, Input{State: Idle, Brightness: 1, Music: half})
	if !tinted(f.Ring[0], musicBass) || !tinted(f.Ring[3], musicBass) || !dark(f.Ring[4:8]) || !tinted(f.Ring[8], musicTreble) || !dark(f.Ring[10:]) {
		t.Fatalf("bands = %v", f.Ring)
	}

	var spectrum Music
	spectrum.Style = MusicSpectrum
	for i := range spectrum.Bands {
		spectrum.Bands[i] = 1
	}
	f = Render(ringOnly, Input{State: Idle, Brightness: 1, Music: &spectrum})
	if !tinted(f.Ring[0], musicHue(0.5/12)) || !tinted(f.Ring[11], musicHue(11.5/12)) {
		t.Fatalf("spectrum = %v", f.Ring)
	}
	for _, px := range f.Ring {
		if px == (RGB{}) {
			t.Fatalf("spectrum left a led dark: %v", f.Ring)
		}
	}

	f = Render(ringOnly, Input{State: Idle, Brightness: 1, Music: &Music{Style: MusicPulse, Breath: 0.8, Presence: 1}})
	for _, px := range f.Ring {
		if px != f.Ring[0] || !tinted(px, musicTreble) {
			t.Fatalf("pulse = %v", f.Ring)
		}
	}

	eq := &Music{Style: MusicEqualizer, Groups: [3]float64{1, 1, 1}, Level: 1, Kick: 1}
	in := at(Idle, 0, 0)
	in.Music = eq
	f = Render(round, in)
	for d := range 4 {
		if f.Digits[d][3] == 0 || f.Digits[d][6] == 0 || f.Digits[d][0] == 0 || f.Digits[d][1] != 0 {
			t.Fatalf("equalizer digit %d = %v", d, f.Digits[d])
		}
	}
	if f.Colon == 0 {
		t.Fatal("the colon missed the kick")
	}
	left := 90
	in.Countdown = &left
	if g := Render(round, in); g.Digits[0][3] == f.Digits[0][3] && g.Digits[0][1] == 0 {
		t.Fatal("equalizer took the digits from a countdown")
	}
}

func ringSum(ring []RGB) int {
	s := 0
	for _, px := range ring {
		s += int(px.R) + int(px.G) + int(px.B)
	}
	return s
}

// a flash brightens the bass arc and fills it, and leaves the other arcs as they were
func TestMusicBandsFlashOnTheKick(t *testing.T) {
	calm := Music{Style: MusicBands, Groups: [3]float64{0.3, 0.5, 0.5}}
	hit := calm
	hit.Flash = 1
	a := Render(ringOnly, Input{State: Idle, Brightness: 1, Music: &calm})
	b := Render(ringOnly, Input{State: Idle, Brightness: 1, Music: &hit})
	if ringSum(b.Ring[:4]) <= ringSum(a.Ring[:4]) || dark(b.Ring[3:4]) {
		t.Fatalf("bass arc %v, then on the kick %v", a.Ring[:4], b.Ring[:4])
	}
	for i := 4; i < 12; i++ {
		if a.Ring[i] != b.Ring[i] {
			t.Fatalf("the kick changed led %d", i)
		}
	}
}

// pulse and beat rest dim with little energy, glow with more, and punch on a flash; beat's hue follows the cycle
func TestMusicPulseAndBeatFollowEnergy(t *testing.T) {
	for _, style := range []MusicStyle{MusicPulse, MusicBeat} {
		quiet := Music{Style: style, Presence: 1, Breath: 0.1}
		loud := quiet
		loud.Breath = 1
		hit := loud
		hit.Flash = 1
		var sums [3]int
		for i, m := range []Music{quiet, loud, hit} {
			sums[i] = ringSum(Render(ringOnly, Input{State: Idle, Brightness: 1, Music: &m}).Ring)
		}
		if sums[0] == 0 || sums[0] >= sums[1] || sums[1] >= sums[2] {
			t.Fatalf("style %d: quiet, loud and flash = %v", style, sums)
		}
	}
	f := Render(ringOnly, Input{State: Idle, Brightness: 1, Music: &Music{Style: MusicBeat, Presence: 1, Breath: 1, Hue: 1.0 / 3}})
	if !tinted(f.Ring[0], musicMid) {
		t.Fatalf("beat at a third of the cycle = %v", f.Ring[0])
	}
	sparkled := Music{Style: MusicBeat, Presence: 1, Breath: 0.2}
	sparkled.Sparkles[0] = MusicSparkle{Pos: 0.5, Bright: 1}
	f = Render(ringOnly, Input{State: Idle, Brightness: 1, Music: &sparkled})
	if brightest(f.Ring[6]) <= brightest(f.Ring[0]) {
		t.Fatalf("no sparkle: %v", f.Ring)
	}
}

// orbit draws each comet's head brightest with its tail behind it, and nothing ahead of it
func TestMusicOrbitDrawsComets(t *testing.T) {
	m := Music{Style: MusicOrbit, Presence: 1}
	m.Comets[0] = MusicComet{Pos: 6.0 / 12, Dir: 1, Bright: 1}
	f := Render(ringOnly, Input{State: Idle, Brightness: 1, Music: &m})
	if !tinted(f.Ring[5], musicBass) || brightest(f.Ring[5]) <= brightest(f.Ring[4]) || brightest(f.Ring[4]) <= brightest(f.Ring[3]) {
		t.Fatalf("orbit = %v", f.Ring)
	}
	if !dark(f.Ring[6:]) || !dark(f.Ring[:2]) {
		t.Fatalf("orbit lit ahead of the head or past the tail: %v", f.Ring)
	}
	m.Comets[0].Dir = -1
	f = Render(ringOnly, Input{State: Idle, Brightness: 1, Music: &m})
	if dark(f.Ring[6:7]) || !dark(f.Ring[:5]) {
		t.Fatalf("orbit backwards = %v", f.Ring)
	}
}

// the orbit turns faster with more energy, and a kick bursts a comet
func TestMusicOrbitSpeedsUpAndBursts(t *testing.T) {
	travel := func(level int) float64 {
		var m musicMeter
		var shown *Music
		// half a second: under a turn even at full speed, so the place is the distance
		for i := range 15 {
			now := frameAt(i)
			// quiet frames under a loud start, so the energy sits low
			v := level
			if i < 3 {
				v = 240
			}
			m.offer(steady(i, v), MusicOrbit, now)
			shown = m.step(now, vizFrame)
		}
		if shown == nil {
			t.Fatal("nothing shown")
		}
		return m.orbit
	}
	if slow, fast := travel(170), travel(240); fast <= slow {
		t.Fatalf("orbit turned %v at low energy and %v at full", slow, fast)
	}
	var m musicMeter
	burst := false
	for i := range 30 * 2 {
		now := frameAt(i)
		m.offer(kickTrack(i, 10, 15, 170, 150, 235), MusicOrbit, now)
		if shown := m.step(now, vizFrame); shown != nil && shown.Comets[1].Bright > 0.5 {
			burst = true
		}
	}
	if !burst {
		t.Fatal("no kick burst a comet")
	}
}

// the meter follows the recent peak, so a quiet track still moves the lights, and the window never falls into noise
func TestMusicMeterFollowsTheRecentPeak(t *testing.T) {
	var m musicMeter
	now := t0
	var shown *Music
	for range 100 {
		now = now.Add(33 * time.Millisecond)
		m.offer(loud(150), MusicBands, now)
		shown = m.step(now, 33*time.Millisecond)
	}
	if shown == nil || shown.Level < 0.9 || shown.Groups[2] < 0.9 {
		t.Fatalf("a steady quiet track must fill the window: %+v", shown)
	}
	for _, style := range []MusicStyle{MusicBands, MusicPulse, MusicSpectrum, MusicEqualizer, MusicOrbit, MusicBeat} {
		var noise musicMeter
		now = t0
		for i := range 100 {
			now = now.Add(33 * time.Millisecond)
			noise.offer(steady(i, 30), style, now)
			if shown = noise.step(now, 33*time.Millisecond); shown != nil {
				t.Fatalf("style %d: near-silence lit the lights: %+v", style, shown)
			}
		}
	}
	var off musicMeter
	off.offer(loud(250), MusicOff, now)
	if off.step(now, 33*time.Millisecond) != nil {
		t.Fatal("style off drew")
	}
}

// the dynamics: a rise shows at once, a fall takes a few frames and speeds up as it goes
func TestMusicRisesAtOnceAndFallsUnderGravity(t *testing.T) {
	var g gravity
	if g.step(0.8, vizFrame) != 0.8 {
		t.Fatal("the rise was not instant")
	}
	a := 0.8 - g.step(0, vizFrame)
	b := 0.8 - a - g.step(0, vizFrame)
	if a <= 0 || b <= a {
		t.Fatalf("falls %v then %v", a, b)
	}
	for range 15 {
		g.step(0, vizFrame)
	}
	if g.v != 0 {
		t.Fatalf("still %v after half a second", g.v)
	}
}
