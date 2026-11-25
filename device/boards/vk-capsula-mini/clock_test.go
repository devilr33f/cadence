package vkcapsula_test

import (
	"os"
	"strconv"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

func readFile(t *testing.T, tr *tree, rel string) string {
	t.Helper()
	b, err := os.ReadFile(tr.path(rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func clockTree(t *testing.T) *tree {
	t.Helper()
	tr := capsulaTree(t)
	for c := 1; c <= 30; c++ {
		tr.write("sys/class/leds/clock-led"+strconv.Itoa(c)+"/brightness", "9\n")
	}
	return tr
}

func clockLED(t *testing.T, tr *tree, ch int) string {
	t.Helper()
	return readFile(t, tr, "sys/class/leds/clock-led"+strconv.Itoa(ch+1)+"/brightness")
}

// segments a..g of 1, 2, 3, 4 and the colon
func digitsOf(shown ...[]int) map[leds.Segment]uint8 {
	out := map[leds.Segment]uint8{leds.Colon: 0, leds.Degree: 0}
	for d := range 4 {
		for s := range 7 {
			out[leds.Segment{Digit: d, Index: s}] = 0
		}
	}
	for d, segs := range shown {
		for _, s := range segs {
			out[leds.Segment{Digit: d, Index: s}] = 100
		}
	}
	return out
}

func TestClockSegmentsReachTheirLEDs(t *testing.T) {
	tr := clockTree(t)
	s := sink(t, tr)
	// "12:34": 1 = b,c; 2 = a,b,d,e,g; 3 = a,b,c,d,g; 4 = b,c,f,g
	lvl := digitsOf([]int{1, 2}, []int{0, 1, 3, 4, 6}, []int{0, 1, 2, 3, 6}, []int{1, 2, 5, 6})
	lvl[leds.Colon] = 50
	lvl[leds.Degree] = 77
	if err := s.Segments(lvl); err != nil {
		t.Fatal(err)
	}
	want := map[int]string{}
	for d, segs := range [][]int{{1, 2}, {0, 1, 3, 4, 6}, {0, 1, 2, 3, 6}, {1, 2, 5, 6}} {
		for _, sg := range segs {
			want[[][7]int{{1, 28, 21, 17, 22, 29, 24}, {26, 25, 19, 18, 23, 27, 20}, {0, 2, 7, 5, 6, 4, 3},
				{10, 12, 11, 14, 13, 8, 9}}[d][sg]] = "100\n"
		}
	}
	want[16], want[15] = "50\n", "50\n"
	for ch := range 30 {
		w, ok := want[ch]
		if !ok {
			w = "0\n"
		}
		if got := clockLED(t, tr, ch); got != w {
			t.Errorf("channel %d = %q, want %q", ch, got, w)
		}
	}
}

func TestOnlyChangedSegmentsAreWritten(t *testing.T) {
	tr := clockTree(t)
	s := sink(t, tr)
	first := digitsOf([]int{1}, []int{2})
	if err := s.Segments(first); err != nil {
		t.Fatal(err)
	}
	// poison every led, then change one segment: only it may be rewritten
	for c := 1; c <= 30; c++ {
		tr.write("sys/class/leds/clock-led"+strconv.Itoa(c)+"/brightness", "poison\n")
	}
	first[leds.Segment{Digit: 0, Index: 1}] = 30
	if err := s.Segments(first); err != nil {
		t.Fatal(err)
	}
	for ch := range 30 {
		w := "poison\n"
		if ch == 28 {
			w = "30\n"
		}
		if got := clockLED(t, tr, ch); got != w {
			t.Errorf("channel %d = %q, want %q", ch, got, w)
		}
	}
	// a failure makes the next frame write everything
	tr.remove("sys/class/leds/clock-led30")
	first[leds.Segment{Digit: 0, Index: 5}] = 31
	if s.Segments(first) == nil {
		t.Fatal("a missing led was not an error")
	}
	tr.write("sys/class/leds/clock-led30/brightness", "poison\n")
	if err := s.Segments(first); err != nil {
		t.Fatal(err)
	}
	if got := clockLED(t, tr, 0); got != "0\n" {
		t.Errorf("channel 0 after a failure = %q", got)
	}
}
