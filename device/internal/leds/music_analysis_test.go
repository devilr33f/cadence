package leds

import (
	"testing"
	"time"
)

// one band frame of audiod's, at 30 a second
const vizFrame = time.Second / 30

// a fixed wobble of a few steps, so steady tones are not perfectly flat
func jitter(frame, band int) int { return (frame*7+band*13)%7 - 3 }

// a track with a kick every `every` frames from frame `first` on: the low bands jump to kickTop and fall back to
// lowRest over a few frames, over a steady bed at bed on every other band
func kickTrack(frame, first, every, bed, lowRest, kickTop int) (b [MusicBandCount]uint8) {
	low := lowRest
	if frame >= first {
		if since := (frame - first) % every; since < 4 {
			low = max(kickTop-since*(kickTop-lowRest)/4, lowRest)
		}
	}
	for i := range b {
		v := bed
		if i < 4 {
			v = low
		}
		b[i] = uint8(min(max(v+jitter(frame, i), 0), 255))
	}
	return b
}

func steady(frame, level int) (b [MusicBandCount]uint8) {
	for i := range b {
		b[i] = uint8(min(max(level+jitter(frame, i), 0), 255))
	}
	return b
}

func frameAt(i int) time.Time { return t0.Add(time.Duration(i) * vizFrame) }

// at 120 bpm every kick is found within a frame of it, and nothing else is a kick
func TestMusicAnalysisFindsEveryKick(t *testing.T) {
	for _, level := range []struct {
		name               string
		bed, rest, kickTop int
	}{{"loud", 170, 150, 235}, {"quiet", 70, 60, 120}} {
		t.Run(level.name, func(t *testing.T) {
			var a musicAnalysis
			const first, every, frames = 10, 15, 30 * 8
			var found []int
			for i := range frames {
				a.push(kickTrack(i, first, every, level.bed, level.rest, level.kickTop), frameAt(i))
				if a.fired[onsetKick] {
					found = append(found, i)
				}
			}
			want := (frames - first + every - 1) / every
			if len(found) != want {
				t.Fatalf("kicks at %v, want %d", found, want)
			}
			for n, i := range found {
				if kick := first + n*every; i < kick || i > kick+1 {
					t.Fatalf("kick %d found at frame %d, played at %d", n, i, kick)
				}
			}
		})
	}
}

// a steady tone, however loud, is no beat
func TestMusicAnalysisSteadyToneHasNoOnsets(t *testing.T) {
	for _, level := range []int{80, 150, 240} {
		var a musicAnalysis
		for i := range 30 * 8 {
			a.push(steady(i, level), frameAt(i))
			if a.fired[onsetKick] || a.fired[onsetSnap] {
				t.Fatalf("level %d: an onset at frame %d", level, i)
			}
		}
		if a.drive > 0 {
			t.Fatalf("level %d: drive %v on a steady tone", level, a.drive)
		}
	}
}

// every band against its own peak: a quiet track reaches full scale, and quiet treble moves as much as loud bass
func TestMusicAnalysisNormalizesEachBand(t *testing.T) {
	var a musicAnalysis
	full := 0.0
	for i := range 30 * 4 {
		var b [MusicBandCount]uint8
		for j := range b {
			// loud bass, treble 30 dB down, both swinging 12 dB
			base := 220
			if j >= 11 {
				base = 100
			}
			b[j] = uint8(base - 50*(i%10)/9)
		}
		a.push(b, frameAt(i))
		if i > 30 {
			full = max(full, a.bands[MusicBandCount-1])
		}
	}
	if full < 0.99 || a.energy <= 0 {
		t.Fatalf("quiet treble peaked at %v (energy %v)", full, a.energy)
	}
	var q musicAnalysis
	for i := range 30 * 4 {
		q.push(steady(i, 110), frameAt(i))
	}
	if q.energy < 0.9 || q.bands[5] < 0.5 {
		t.Fatalf("a quiet steady track shows energy %v, band %v", q.energy, q.bands[5])
	}
}

// silence and noise under -45 dBFS are dark
func TestMusicAnalysisSilenceIsDark(t *testing.T) {
	var a musicAnalysis
	for i := range 30 * 4 {
		a.push(steady(i, 40+3*(i%5)), frameAt(i))
		if a.energy != 0 || a.fired[onsetKick] || a.fired[onsetSnap] {
			t.Fatalf("noise at frame %d: energy %v, onsets %v", i, a.energy, a.fired)
		}
		for _, v := range a.bands {
			if v != 0 {
				t.Fatalf("noise lit a band at frame %d: %v", i, a.bands)
			}
		}
	}
}

// a chorus 12 dB over the verse is a drop, once
func TestMusicAnalysisFindsADrop(t *testing.T) {
	var a musicAnalysis
	drops := 0
	var last time.Time
	for i := range 30 * 16 {
		level := 120
		if i >= 30*10 {
			level = 170
		}
		a.push(steady(i, level), frameAt(i))
		if a.drop != last {
			drops, last = drops+1, a.drop
			if i < 30*10 || i > 30*10+15 {
				t.Fatalf("drop at frame %d", i)
			}
		}
	}
	if drops != 1 {
		t.Fatalf("drops = %d", drops)
	}
}

// kicks faster than 3 a second still flash the whole ring at most 3 times in any second
func TestMusicFlashRateIsCapped(t *testing.T) {
	var m musicMeter
	kicks, flashes := 0, []time.Time{}
	var lastFlash time.Time
	for i := range 30 * 6 {
		now := frameAt(i)
		m.offer(kickTrack(i, 3, 4, 170, 150, 240), MusicBeat, now)
		if m.an.fired[onsetKick] {
			kicks++
		}
		if m.flashAt != lastFlash {
			lastFlash = m.flashAt
			flashes = append(flashes, m.flashAt)
		}
		m.step(now, vizFrame)
	}
	if kicks < 6*6 {
		t.Fatalf("only %d kicks: the test needs kicks faster than the cap", kicks)
	}
	for i := range flashes {
		if i > 0 && flashes[i].Sub(flashes[i-1]) < flashGap {
			t.Fatalf("flashes %v apart", flashes[i].Sub(flashes[i-1]))
		}
		inSecond := 0
		for _, f := range flashes[i:] {
			if f.Sub(flashes[i]) < time.Second {
				inSecond++
			}
		}
		if inSecond > 3 {
			t.Fatalf("%d flashes within a second", inSecond)
		}
	}
	if len(flashes) < 6*2 {
		t.Fatalf("only %d flashes", len(flashes))
	}
}

// what the ring shows agrees: the beat and pulse styles' whole-ring brightness jumps at most 3 times a second
func TestMusicWholeRingFlashesAtMostThreeASecond(t *testing.T) {
	for _, style := range []MusicStyle{MusicBeat, MusicPulse} {
		var m musicMeter
		prev := 0.0
		var jumps []time.Time
		for i := range 30 * 6 {
			now := frameAt(i)
			m.offer(kickTrack(i, 3, 4, 170, 150, 240), style, now)
			f := Render(ringOnly, Input{State: Idle, Brightness: 1, Music: m.step(now, vizFrame)})
			sum := 0.0
			for _, px := range f.Ring {
				sum += float64(brightest(px))
			}
			mean := sum / float64(len(f.Ring))
			if mean-prev > 40 {
				jumps = append(jumps, now)
			}
			prev = mean
		}
		if len(jumps) < 6 {
			t.Fatalf("style %d: only %d flashes", style, len(jumps))
		}
		for i := range jumps {
			if i > 0 && jumps[i].Sub(jumps[i-1]) < flashGap {
				t.Fatalf("style %d: flashes %v apart", style, jumps[i].Sub(jumps[i-1]))
			}
		}
	}
}

// hats on the high bands are snaps, not kicks
func TestMusicAnalysisFindsSnapsApartFromKicks(t *testing.T) {
	var a musicAnalysis
	snaps := 0
	for i := range 30 * 4 {
		b := steady(i, 150)
		if i >= 5 && (i-5)%8 == 0 {
			for j := 8; j < MusicBandCount; j++ {
				b[j] = 215
			}
		}
		a.push(b, frameAt(i))
		if a.fired[onsetKick] {
			t.Fatalf("a hat at frame %d was a kick", i)
		}
		if a.fired[onsetSnap] {
			if (i-5)%8 != 0 {
				t.Fatalf("a snap at frame %d", i)
			}
			snaps++
		}
	}
	if want := (30*4 - 5 + 7) / 8; snaps != want {
		t.Fatalf("snaps = %d, want %d", snaps, want)
	}
}
