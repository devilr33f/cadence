package pickle_test

import (
	"runtime"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

const clock = "sys/class/leds/pickle:white:clock"

// clockTree has every clock led, each holding 9 so a write shows
func clockTree(t *testing.T) *tree {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the clock's led names hold colons, which windows file names cannot")
	}
	tr := pickleTree(t)
	for _, led := range clockLEDs() {
		tr.write(clock+led+"/brightness", "9\n")
	}
	return tr
}

func clockLEDs() []string {
	leds := []string{"DM", "DEGREE", "DH", "DL"}
	for _, letter := range "ABCDEFGH" {
		for _, digit := range "0123" {
			leds = append(leds, string(letter)+string(digit))
		}
	}
	return leds
}

// level is what the renderer hands over: a..g of every digit, the colon and the degree dot
func level(set map[leds.Segment]uint8) map[leds.Segment]uint8 {
	out := map[leds.Segment]uint8{leds.Colon: 0, leds.Degree: 0}
	for d := range 4 {
		for s := range 7 {
			out[leds.Segment{Digit: d, Index: s}] = 0
		}
	}
	for k, v := range set {
		out[k] = v
	}
	return out
}

func (tr *tree) clock(led string) string {
	tr.t.Helper()
	return tr.read(clock + led + "/brightness")
}

func TestSegmentsReachTheirLEDs(t *testing.T) {
	tr := clockTree(t)
	s := sink(t, tr)
	err := s.Segments(level(map[leds.Segment]uint8{
		{Digit: 0, Index: 0}: 200, {Digit: 3, Index: 6}: 17, {Digit: 2, Index: 1}: 1, leds.Colon: 120, leds.Degree: 55,
	}))
	if err != nil {
		t.Fatal(err)
	}
	// segment a..g of digit d is <letter><d>; the decimal point (H) is unused and dark; the colon is both halves
	for led, want := range map[string]string{"A0": "200\n", "G3": "17\n", "B2": "1\n", "DH": "120\n", "DL": "120\n",
		"DEGREE": "55\n", "DM": "0\n", "H0": "0\n", "H3": "0\n", "C1": "0\n", "A3": "0\n"} {
		if got := tr.clock(led); got != want {
			t.Errorf("%s = %q, want %q", led, got, want)
		}
	}
	for _, led := range clockLEDs() {
		if tr.clock(led) == "9\n" {
			t.Errorf("%s was never written", led)
		}
	}
}

func TestOnlyChangedSegmentsAreWritten(t *testing.T) {
	tr := clockTree(t)
	s := sink(t, tr)
	first := level(map[leds.Segment]uint8{{Digit: 1, Index: 2}: 80, leds.Colon: 120})
	if err := s.Segments(first); err != nil {
		t.Fatal(err)
	}
	// marks: an led written again shows its value instead
	for _, led := range clockLEDs() {
		tr.write(clock+led+"/brightness", "9\n")
	}
	second := level(map[leds.Segment]uint8{{Digit: 1, Index: 2}: 80, {Digit: 0, Index: 4}: 30, leds.Colon: 0})
	if err := s.Segments(second); err != nil {
		t.Fatal(err)
	}
	for _, led := range clockLEDs() {
		want := "9\n"
		switch led {
		case "E0":
			want = "30\n"
		case "DH", "DL":
			want = "0\n"
		}
		if got := tr.clock(led); got != want {
			t.Errorf("%s = %q, want %q", led, got, want)
		}
	}
}

func TestAFailedClockWriteRewritesEveryLED(t *testing.T) {
	tr := clockTree(t)
	s := sink(t, tr)
	if err := s.Segments(level(nil)); err != nil {
		t.Fatal(err)
	}
	tr.remove(clock + "C2")
	if r := reason(s.Segments(level(map[leds.Segment]uint8{{Digit: 2, Index: 2}: 50}))); r != "clock_write_failed" {
		t.Fatalf("a missing led: %q", r)
	}
	tr.write(clock+"C2/brightness", "9\n")
	for _, led := range clockLEDs() {
		tr.write(clock+led+"/brightness", "9\n")
	}
	// part of the clock may hold anything now, so the next frame writes it whole, even one equal to the last good one
	if err := s.Segments(level(nil)); err != nil {
		t.Fatal(err)
	}
	for _, led := range clockLEDs() {
		if got := tr.clock(led); got != "0\n" {
			t.Errorf("%s = %q", led, got)
		}
	}
}
