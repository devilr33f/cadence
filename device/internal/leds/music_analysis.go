package leds

import (
	"math"
	"time"
)

// the analysis reads audiod's 16 bands (0..1 here, over -60..0 dBFS, so a step of 0.1 is 6 dB) as they come, about
// 30 a second: every band normalized to its own recent peak, onsets (a kick on the low bands, a snap on the highs) from
// the half-wave rectified flux of the log bands against an adaptive threshold, and the overall energy. it is pure: the
// time comes with each frame.

const (
	// nothing under -45 dBFS shows, however quiet the track: the window never reaches into noise
	musicNoise = 0.25
	// each band's recent peak falls 3 dB a second, never under -38 dBFS: a band that only hisses stays dark
	bandPeakFall  = 0.05
	bandPeakFloor = 0.37
	// the window shown below a band's peak: 24 dB
	bandSpan = 0.4
	// a band's normalized value is raised to this: quiet parts of the window sink, peaks stand out
	bandContrast = 1.5

	// the loudness peak falls 1.2 dB a second, so a quiet verse dims against the chorus before it
	loudPeakFall  = 0.02
	loudPeakFloor = 0.4
	loudSpan      = 0.3

	// the energy's slow follower, and the short and long loudness averages a drop is measured between
	energySlowTau = 1500 * time.Millisecond
	loudShortTau  = 250 * time.Millisecond
	loudLongTau   = 8 * time.Second
	// drive is 0 until the short loudness is 3 dB over the long one, 1 from 10 dB over
	driveFrom  = 0.05
	driveRange = 0.12
	// a drop is drive passing this, at most once per dropGap
	dropAt  = 0.7
	dropGap = 4 * time.Second

	// the flux history the threshold is taken from: 1.5 s of frames
	fluxHistory = 45
	// a frame later than this after the one before starts the analysis over
	analysisGap = musicStale
)

// the onset classes: the kick on 60-171 Hz, the snap (snare, hats) on 1-16 kHz
const (
	onsetKick = iota
	onsetSnap
	onsetClasses
)

type onsetClass struct {
	bands [2]int
	// the threshold is mean + k*deviation of the recent flux, and never under min (mean dB per band)
	k, min float64
	// no second onset sooner than this
	refractory time.Duration
	// how fast the class's pulse fades
	decay time.Duration
}

var onsetClassesOf = [onsetClasses]onsetClass{
	onsetKick: {bands: [2]int{0, 3}, k: 1.5, min: 0.04, refractory: 120 * time.Millisecond, decay: 140 * time.Millisecond},
	onsetSnap: {bands: [2]int{8, MusicBandCount}, k: 1.8, min: 0.035, refractory: 80 * time.Millisecond, decay: 90 * time.Millisecond},
}

// onsetDetector finds one class's onsets in its flux
type onsetDetector struct {
	class    onsetClass
	history  [fluxHistory]float64
	n, next  int
	lastFlux float64
	// the last onset, and its strength 0..1
	at       time.Time
	strength float64
}

// push takes one frame's flux; true when it is an onset
func (d *onsetDetector) push(flux float64, now time.Time) bool {
	mean, dev := 0.0, 0.0
	if d.n > 0 {
		for _, f := range d.history[:d.n] {
			mean += f
		}
		mean /= float64(d.n)
		for _, f := range d.history[:d.n] {
			dev += (f - mean) * (f - mean)
		}
		dev = math.Sqrt(dev / float64(d.n))
	}
	threshold := max(mean+d.class.k*dev, d.class.min)
	rising := flux > d.lastFlux
	d.lastFlux = flux
	d.history[d.next] = flux
	d.next = (d.next + 1) % fluxHistory
	d.n = min(d.n+1, fluxHistory)
	if flux <= threshold || !rising || !d.at.IsZero() && now.Sub(d.at) < d.class.refractory {
		return false
	}
	d.at = now
	d.strength = clamp01(0.6 + 0.4*(flux-threshold)/threshold)
	return true
}

// pulse is the class's decaying beat at now: its strength at the onset, falling away
func (d *onsetDetector) pulse(now time.Time) float64 {
	if d.at.IsZero() || now.Before(d.at) {
		return 0
	}
	return d.strength * math.Exp(-float64(now.Sub(d.at))/float64(d.class.decay))
}

// musicAnalysis is what the frames so far say. only the renderer's stepping goroutine touches it, under its lock.
type musicAnalysis struct {
	at   time.Time
	prev [MusicBandCount]float64

	bandPeak [MusicBandCount]float64
	// each band 0..1 against its own peak, after the contrast curve
	bands [MusicBandCount]float64

	loudPeak  float64
	loudShort float64
	loudLong  float64
	// 0..1 against the recent loudness, its slow follower, and how far the loudness is over its long average
	energy float64
	slow   float64
	drive  float64
	drop   time.Time

	onsets [onsetClasses]onsetDetector
	// the onsets of the latest frame
	fired [onsetClasses]bool
}

func (a *musicAnalysis) reset() {
	*a = musicAnalysis{}
	for c := range a.onsets {
		a.onsets[c].class = onsetClassesOf[c]
	}
}

// push analyses one frame of raw bands that arrived at now
func (a *musicAnalysis) push(raw [MusicBandCount]uint8, now time.Time) {
	if a.at.IsZero() || now.Sub(a.at) > analysisGap || now.Before(a.at) {
		a.reset()
	}
	first := a.at.IsZero()
	dt := 0.0
	if !first {
		dt = now.Sub(a.at).Seconds()
	}
	a.at = now

	var x [MusicBandCount]float64
	loud := 0.0
	for i, b := range raw {
		x[i] = max(float64(b)/255, musicNoise)
		loud += x[i]
	}
	loud /= MusicBandCount

	for i := range x {
		a.bandPeak[i] = max(a.bandPeak[i]-bandPeakFall*dt, x[i], bandPeakFloor)
		bottom := max(a.bandPeak[i]-bandSpan, musicNoise)
		a.bands[i] = math.Pow(clamp01((x[i]-bottom)/(a.bandPeak[i]-bottom)), bandContrast)
	}

	a.loudPeak = max(a.loudPeak-loudPeakFall*dt, loud, loudPeakFloor)
	bottom := max(a.loudPeak-loudSpan, musicNoise)
	a.energy = clamp01((loud - bottom) / (a.loudPeak - bottom))
	elapsed := time.Duration(dt * float64(time.Second))
	if first {
		a.slow, a.loudShort, a.loudLong = a.energy, loud, loud
	} else {
		a.slow = approach(a.slow, a.energy, elapsed, energySlowTau)
		a.loudShort = approach(a.loudShort, loud, elapsed, loudShortTau)
		a.loudLong = approach(a.loudLong, loud, elapsed, loudLongTau)
	}
	drive := clamp01((a.loudShort - a.loudLong - driveFrom) / driveRange)
	if drive >= dropAt && a.drive < dropAt && (a.drop.IsZero() || now.Sub(a.drop) >= dropGap) {
		a.drop = now
	}
	a.drive = drive

	for c := range a.onsets {
		a.fired[c] = false
		if first {
			continue
		}
		r := a.onsets[c].class.bands
		flux := 0.0
		for i := r[0]; i < r[1]; i++ {
			flux += max(x[i]-a.prev[i], 0)
		}
		a.fired[c] = a.onsets[c].push(flux/float64(r[1]-r[0]), now)
	}
	a.prev = x
}

// kick and snap are the classes' pulses at now
func (a *musicAnalysis) kick(now time.Time) float64 { return a.onsets[onsetKick].pulse(now) }
func (a *musicAnalysis) snap(now time.Time) float64 { return a.onsets[onsetSnap].pulse(now) }

// dropped says whether a drop came within d before now
func (a *musicAnalysis) dropped(now time.Time, d time.Duration) bool {
	return !a.drop.IsZero() && now.Sub(a.drop) < d
}
