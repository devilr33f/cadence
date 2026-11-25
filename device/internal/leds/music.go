package leds

import (
	"math"
	"time"
)

// MusicStyle is how the idle lights show music.
type MusicStyle uint8

const (
	MusicOff MusicStyle = iota
	// three arcs: bass, mid and treble (v1's default), the bass arc flashing on kicks and the treble on snaps
	MusicBands
	// the whole ring breathes with the energy and punches on kicks
	MusicPulse
	// every band in its place along the ring, bass to treble
	MusicSpectrum
	// the bands, and the clock's digits as a meter while no countdown owns them
	MusicEqualizer
	// comets run around the ring, faster with the energy; a kick bursts a new one
	MusicOrbit
	// the whole ring steps its hue and flashes on kicks, sparkles on snaps
	MusicBeat
)

// MusicBandCount is the number of spectrum bands audiod sends, log-spaced from 60 Hz to 16 kHz.
const MusicBandCount = 16

const (
	musicComets   = 4
	musicSparkles = 4
)

// MusicComet is one comet of orbit: its head's place 0..1 around the ring, its direction (+1 or -1), brightness and
// place on the palette's cycle.
type MusicComet struct {
	Pos, Dir, Bright, Hue float64
}

// MusicSparkle is one led lit by a snap: its place 0..1 around the ring and its brightness.
type MusicSparkle struct {
	Pos, Bright float64
}

// Music is what the idle state draws of the music playing: every value 0..1, the bands and energy relative to their
// own recent peaks.
type Music struct {
	Style MusicStyle
	// each band against its own recent peak
	Bands [MusicBandCount]float64
	// bass, mid and treble
	Groups [3]float64
	// the energy: the loudness against its recent peak
	Level float64
	// the energy eased in and out, for what lights the whole ring: it never jumps with a kick
	Breath float64
	// how far the loudness is over its long average: a drop
	Drive float64
	// the kick's and the snap's decaying beat
	Kick, Snap float64
	// a whole-ring flash: a kick's, never more often than flashGap
	Flash float64
	// the place on the palette's cycle, stepped by flashes and drifting with the energy
	Hue      float64
	Comets   [musicComets]MusicComet
	Sparkles [musicSparkles]MusicSparkle
	// 1 while music sounds, falling to 0 when it stops or goes silent
	Presence float64
}

const (
	// a frame older than this is music that stopped
	musicStale = 250 * time.Millisecond
	// falling values speed up as they fall, like a meter's needle: 1 to 0 in about 0.45 s
	musicGravity = 10.0
	// presence eases in when music starts and fades out once it goes silent
	presenceAttack  = 150 * time.Millisecond
	presenceRelease = 400 * time.Millisecond
	// breath eases the energy in and out
	breathAttack  = 120 * time.Millisecond
	breathRelease = 400 * time.Millisecond
	// smoothed values under this are nothing to draw
	musicVisible = 0.01

	// the photosensitivity limit: no whole-ring flash within this of the one before, under 3 a second
	flashGap   = 350 * time.Millisecond
	flashDecay = 130 * time.Millisecond
	// a flash steps the hue this far along the palette's cycle, a flash right after a drop much further
	hueStep     = 1.0 / 12
	hueDropStep = 1.0 / 3
	dropHue     = 600 * time.Millisecond
	// the hue drifts this many cycles a second at full energy
	hueDrift = 0.02

	// the orbit's resting comet turns this often a second, up to orbitFast with the energy and drive
	orbitSlow = 0.15
	orbitFast = 1.1
	// a kick's comet turns this much faster than the resting one and fades over cometLife
	burstSpeed = 0.6
	cometLife  = 500 * time.Millisecond
	sparkLife  = 160 * time.Millisecond
)

// the groups of the 16 bands: 60-240 Hz, 240 Hz-2.8 kHz, 2.8-16 kHz
var musicGroups = [3][2]int{{0, 4}, {4, 11}, {11, MusicBandCount}}

// a value that jumps up at once and falls under gravity
type gravity struct{ v, vel float64 }

func (g *gravity) step(target float64, elapsed time.Duration) float64 {
	if target >= g.v {
		g.v, g.vel = target, 0
		return g.v
	}
	dt := elapsed.Seconds()
	g.vel += musicGravity * dt
	g.v = max(target, g.v-g.vel*dt)
	return g.v
}

type burst struct {
	at       time.Time
	pos, dir float64
	strength float64
	hue      float64
}

// musicMeter turns raw band frames into what the lights draw: the analysis on each frame, the dynamics and the
// animations on each step. only the renderer's stepping goroutine touches it, under the renderer's lock.
type musicMeter struct {
	at    time.Time
	style MusicStyle
	an    musicAnalysis

	bands    [MusicBandCount]gravity
	groups   [3]gravity
	level    gravity
	presence float64
	breath   float64

	flashAt       time.Time
	flashStrength float64
	hue           float64
	orbit         float64
	bursts        [musicComets - 1]burst
	nextDir       float64
	sparks        [musicSparkles]burst
	seed          uint32
}

func (m *musicMeter) offer(raw [MusicBandCount]uint8, style MusicStyle, now time.Time) {
	m.at, m.style = now, style
	if style == MusicOff {
		return
	}
	m.an.push(raw, now)
	if m.an.fired[onsetKick] {
		m.kicked(now)
	}
	if m.an.fired[onsetSnap] {
		m.snapped(now)
	}
}

// a kick flashes the ring and steps the hue unless a flash showed within flashGap, and always bursts a comet
func (m *musicMeter) kicked(now time.Time) {
	strength := m.an.onsets[onsetKick].strength
	if m.flashAt.IsZero() || now.Sub(m.flashAt) >= flashGap || now.Before(m.flashAt) {
		m.flashAt, m.flashStrength = now, strength
		step := hueStep
		if m.an.dropped(now, dropHue) {
			step = hueDropStep
		}
		m.hue = math.Mod(m.hue+step, 1)
	}
	if m.nextDir == 0 {
		m.nextDir = -1
	}
	i := weakest(m.bursts[:], now, cometLife)
	m.bursts[i] = burst{at: now, pos: m.orbit, dir: m.nextDir, strength: strength, hue: math.Mod(m.hue+1.0/3, 1)}
	m.nextDir = -m.nextDir
}

// a snap lights a led somewhere on the ring
func (m *musicMeter) snapped(now time.Time) {
	i := weakest(m.sparks[:], now, sparkLife)
	m.sparks[i] = burst{at: now, pos: m.random(), strength: m.an.onsets[onsetSnap].strength}
}

// the dimmest of bs at now, to be replaced
func weakest(bs []burst, now time.Time, life time.Duration) int {
	w, least := 0, math.Inf(1)
	for i, b := range bs {
		if v := b.brightness(now, life); v < least {
			w, least = i, v
		}
	}
	return w
}

func (b burst) brightness(now time.Time, life time.Duration) float64 {
	if b.at.IsZero() || now.Before(b.at) {
		return 0
	}
	return b.strength * math.Exp(-float64(now.Sub(b.at))/float64(life))
}

// random is 0..1, the same sequence every run: xorshift
func (m *musicMeter) random() float64 {
	if m.seed == 0 {
		m.seed = 0x9e3779b9
	}
	m.seed ^= m.seed << 13
	m.seed ^= m.seed >> 17
	m.seed ^= m.seed << 5
	return float64(m.seed) / (1 << 32)
}

// flash is the whole-ring flash at now
func (m *musicMeter) flash(now time.Time) float64 {
	if m.flashAt.IsZero() || now.Before(m.flashAt) {
		return 0
	}
	return m.flashStrength * math.Exp(-float64(now.Sub(m.flashAt))/float64(flashDecay))
}

// step advances the dynamics to now; nil when there is nothing to draw
func (m *musicMeter) step(now time.Time, elapsed time.Duration) *Music {
	fresh := m.style != MusicOff && !m.at.IsZero() && now.Sub(m.at) < musicStale
	var target [MusicBandCount]float64
	energy, present := 0.0, 0.0
	if fresh {
		target, energy = m.an.bands, m.an.energy
		present = clamp01(m.an.energy * 4)
	}
	m.presence = ease(m.presence, present, elapsed, presenceAttack, presenceRelease)
	out := Music{Style: m.style, Presence: m.presence}
	visible := m.presence > musicVisible
	for i := range m.bands {
		out.Bands[i] = m.bands[i].step(target[i], elapsed)
		visible = visible || out.Bands[i] > musicVisible
	}
	for g, r := range musicGroups {
		mean := 0.0
		for i := r[0]; i < r[1]; i++ {
			mean += target[i]
		}
		out.Groups[g] = m.groups[g].step(mean/float64(r[1]-r[0]), elapsed)
	}
	out.Level = m.level.step(energy, elapsed)
	m.breath = ease(m.breath, energy, elapsed, breathAttack, breathRelease)
	out.Breath = m.breath
	if fresh {
		out.Drive = m.an.drive
	}
	out.Kick, out.Snap, out.Flash = m.an.kick(now), m.an.snap(now), m.flash(now)
	visible = visible || out.Level > musicVisible || out.Kick > musicVisible || out.Flash > musicVisible

	dt := elapsed.Seconds()
	m.hue = math.Mod(m.hue+hueDrift*out.Level*dt, 1)
	out.Hue = m.hue
	speed := orbitSlow + (orbitFast-orbitSlow)*clamp01(out.Level*0.8+out.Drive*0.4)
	m.orbit = wrap01(m.orbit + speed*dt)
	out.Comets[0] = MusicComet{Pos: m.orbit, Dir: 1, Bright: m.presence * (0.35 + 0.65*out.Breath), Hue: m.hue}
	for i := range m.bursts {
		b := &m.bursts[i]
		b.pos = wrap01(b.pos + b.dir*(speed+burstSpeed)*dt)
		bright := b.brightness(now, cometLife)
		out.Comets[i+1] = MusicComet{Pos: b.pos, Dir: b.dir, Bright: bright, Hue: b.hue}
		visible = visible || bright > musicVisible
	}
	for i, s := range m.sparks {
		out.Sparkles[i] = MusicSparkle{Pos: s.pos, Bright: s.brightness(now, sparkLife)}
		visible = visible || out.Sparkles[i].Bright > musicVisible
	}
	if !visible {
		return nil
	}
	return &out
}

func ease(cur, target float64, elapsed, attack, release time.Duration) float64 {
	if target > cur {
		return approach(cur, target, elapsed, attack)
	}
	return approach(cur, target, elapsed, release)
}

func wrap01(v float64) float64 { return v - math.Floor(v) }
