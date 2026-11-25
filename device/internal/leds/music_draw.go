package leds

import "math"

// the hues of music: bass, mid and treble, around which the palette's cycle runs, and the warm white of a flash
var (
	musicBass   = RGB{180, 70, 255}
	musicMid    = RGB{0, 230, 170}
	musicTreble = RGB{60, 130, 255}
	musicWhite  = RGB{255, 240, 225}
)

const (
	// a lit arc's least intensity: an arc with any music in it is clearly on
	arcFloor = 0.4
	// how much of a flash whitens and brightens what it lights
	flashWhite  = 0.5
	flashBright = 0.6
	// pulse and beat rest this bright with no energy, and rise this much with full energy
	pulseRest   = 0.12
	pulseEnergy = 0.5
	beatRest    = 0.1
	beatEnergy  = 0.4
	// orbit's comets trail a quarter of the ring, at least 3 leds
	orbitTail    = 0.25
	orbitFalloff = 1.6
	orbitGlow    = 0.08
)

// drawMusic decorates an idle frame that already shows its clock. it never touches the button or the alarm dot
func drawMusic(f *Frame, m *Music, countdown bool, paint painter) {
	if m == nil {
		return
	}
	switch m.Style {
	case MusicBands:
		musicArcs(f.Ring, m, paint)
	case MusicPulse:
		musicPulse(f.Ring, m, paint)
	case MusicSpectrum:
		musicSpectrum(f.Ring, m, paint)
	case MusicEqualizer:
		musicArcs(f.Ring, m, paint)
		if !countdown {
			musicDigits(f, m, paint)
		}
	case MusicOrbit:
		musicOrbit(f.Ring, m, paint)
	case MusicBeat:
		musicBeat(f.Ring, m, paint)
	}
}

// each group lights its third of the ring from its first led: the fill runs value*length leds, the edge one
// fractionally. a flash fills the bass arc and whitens it, a snap whitens the treble a little
func musicArcs(ring []RGB, m *Music, paint painter) {
	n := len(ring)
	colours := [3]RGB{musicBass, musicMid, musicTreble}
	accents := [3]float64{m.Flash, 0, 0.5 * m.Snap}
	for g := range 3 {
		first, next := g*n/3, (g+1)*n/3
		length := next - first
		if length <= 0 {
			continue
		}
		a := accents[g]
		v := max(m.Groups[g], a)
		if v <= 0 {
			for i := range length {
				ring[first+i] = RGB{}
			}
			continue
		}
		colour := towardsRGB(colours[g], musicWhite, flashWhite*a)
		intensity := clamp01(arcFloor + (1-arcFloor)*v + flashBright*a)
		lit := v * float64(length)
		for i := range length {
			ring[first+i] = paint.colour(colour, intensity*clamp01(lit-float64(i)))
		}
	}
}

// the whole ring in the cycle's colour (treble's at rest), breathing with the energy and punching on flashes
func musicPulse(ring []RGB, m *Music, paint painter) {
	colour := towardsRGB(musicCycle(2.0/3+m.Hue), musicWhite, flashWhite*m.Flash)
	fill(ring, paint.colour(colour, m.Presence*(pulseRest+pulseEnergy*m.Breath)+flashBright*m.Flash))
}

// every led shows the band at its place along the ring, interpolated between neighbours, in a hue from bass to
// treble; a flash lifts the bass end
func musicSpectrum(ring []RGB, m *Music, paint painter) {
	n := len(ring)
	for i := range n {
		pos := (float64(i) + 0.5) / float64(n)
		at := pos*MusicBandCount - 0.5
		lo := int(math.Floor(at))
		frac := at - float64(lo)
		v := (1-frac)*m.Bands[min(max(lo, 0), MusicBandCount-1)] + frac*m.Bands[min(max(lo+1, 0), MusicBandCount-1)]
		lift := m.Flash * (1 - pos) * (1 - pos)
		ring[i] = paint.colour(towardsRGB(musicHue(pos), musicWhite, flashWhite*lift), v+flashBright*lift)
	}
}

// bass purple through mid green to treble blue
func musicHue(pos float64) RGB {
	if pos < 0.5 {
		return towardsRGB(musicBass, musicMid, pos*2)
	}
	return towardsRGB(musicMid, musicTreble, (pos-0.5)*2)
}

// the palette as a cycle: bass at 0, mid at 1/3, treble at 2/3, back to bass at 1
func musicCycle(h float64) RGB {
	h = wrap01(h) * 3
	stops := [4]RGB{musicBass, musicMid, musicTreble, musicBass}
	i := min(int(h), 2)
	return towardsRGB(stops[i], stops[i+1], h-float64(i))
}

// the comets: each head at its place with a tail behind it against its direction, the brightest comet owning a led,
// over a faint glow of the energy
func musicOrbit(ring []RGB, m *Music, paint painter) {
	n := len(ring)
	if n == 0 {
		return
	}
	tail := max(orbitTail*float64(n), 3)
	glow := paint.colour(musicCycle(m.Hue), m.Presence*orbitGlow*m.Breath)
	for i := range n {
		ring[i] = glow
		best := 0.0
		for _, c := range m.Comets {
			if c.Bright <= 0 {
				continue
			}
			// how far the led lies behind the head, in leds
			d := math.Mod((c.Pos*float64(n)-(float64(i)+0.5))*c.Dir, float64(n))
			if d < 0 {
				d += float64(n)
			}
			if d >= tail {
				continue
			}
			v := c.Bright * math.Pow(1-d/tail, orbitFalloff)
			if v > best {
				best = v
				ring[i] = paint.colour(musicCycle(c.Hue), v)
			}
		}
	}
}

// the whole ring in the cycle's colour, resting with the energy, flashing white-ish on flashes; snaps light sparkles
func musicBeat(ring []RGB, m *Music, paint painter) {
	n := len(ring)
	if n == 0 {
		return
	}
	colour := towardsRGB(musicCycle(m.Hue), musicWhite, flashWhite*m.Flash)
	fill(ring, paint.colour(colour, m.Presence*(beatRest+beatEnergy*m.Breath)+flashBright*m.Flash))
	spark := towardsRGB(musicTreble, musicWhite, 0.6)
	for _, s := range m.Sparkles {
		if s.Bright <= musicVisible {
			continue
		}
		i := min(int(s.Pos*float64(n)), n-1)
		if px := paint.colour(spark, s.Bright); brightest(px) > brightest(ring[i]) {
			ring[i] = px
		}
	}
}

func brightest(p RGB) int { return int(max(p.R, p.G, p.B)) }

// the four digits meter bass, mid, treble and the energy, taking the clock's place: each fills d, g, then a from the
// bottom, a third of its value each with a fractional edge; the colon beats with the kick
func musicDigits(f *Frame, m *Music, paint painter) {
	if len(f.Digits) != 4 {
		return
	}
	rungs := [3]int{3, 6, 0}
	values := [4]float64{m.Groups[0], m.Groups[1], m.Groups[2], m.Level}
	f.Colon = paint.segment(m.Kick)
	for d, v := range values {
		clear(f.Digits[d])
		for r, s := range rungs {
			f.Digits[d][s] = paint.segment(v*3 - float64(r))
		}
	}
}
