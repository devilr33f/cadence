package main

import (
	"math"
	"testing"
)

// stereoDrop is 48 kHz stereo: 1000 Hz left at -12 dBFS that drops by 16.5 dB
// at dropAt frames, and 1500 Hz right at -12 dBFS throughout.
func stereoDrop(frames, dropAt int) []int16 {
	loud := math.Pow(10, -12.0/20)
	quiet := math.Pow(10, -28.5/20)
	s := make([]int16, 2*frames)
	for i := range frames {
		amp := loud
		if i >= dropAt {
			amp = quiet
		}
		t := float64(i) / 48000
		s[2*i] = int16(math.Round(amp * 32768 * math.Sin(2*math.Pi*1000*t)))
		s[2*i+1] = int16(math.Round(loud * 32768 * math.Sin(2*math.Pi*1500*t)))
	}
	return s
}

func TestToneLevelsSeeTheDropInItsWindows(t *testing.T) {
	// 1.05 s: ten whole 100 ms windows and a partial one, which is left out
	p, err := parse(wavChannels(1, 16, 2, stereoDrop(50400, 24000)))
	if err != nil {
		t.Fatal(err)
	}
	levels, err := toneLevels(p, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 10 {
		t.Fatalf("%d windows, want 10", len(levels))
	}
	for i, w := range levels {
		want := -12.0
		if i >= 5 {
			want = -28.5
		}
		if w.startMs != i*100 || len(w.dbfs) != 2 {
			t.Fatalf("window %d = %+v", i, w)
		}
		if math.Abs(w.dbfs[0]-want) > 0.2 {
			t.Errorf("window %d left = %.2f dBFS, want %.1f", i, w.dbfs[0], want)
		}
		// the right channel carries 1500 Hz only
		if w.dbfs[1] > -60 {
			t.Errorf("window %d right = %.2f dBFS at 1000 Hz", i, w.dbfs[1])
		}
	}
}

func TestToneLevelsSilenceHasAFloor(t *testing.T) {
	p, err := parse(wav(1, 16, make([]int16, 4800)))
	if err != nil {
		t.Fatal(err)
	}
	levels, err := toneLevels(p, 1000, 100)
	if err != nil || len(levels) != 1 || levels[0].dbfs[0] != dbfsFloor {
		t.Fatalf("levels = %+v, err = %v", levels, err)
	}
}

func TestToneLevelsRejectBadArguments(t *testing.T) {
	p, err := parse(wav(1, 16, tone(4800, 0.3)))
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]struct {
		hz float64
		ms int
	}{"no tone": {0, 100}, "at nyquist": {24000, 100}, "no window": {1000, 0}} {
		if _, err := toneLevels(p, args.hz, args.ms); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestFormatLevel(t *testing.T) {
	if got := formatLevel(windowLevel{startMs: 100, dbfs: []float64{-22.46, dbfsFloor}}); got != "ms=100 ch0=-22.5 ch1=-120.0" {
		t.Fatalf("got %q", got)
	}
}
