package vkcapsula_test

import (
	"strconv"
	"testing"

	vkcapsula "git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

func TestRingChannelsPermutation(t *testing.T) {
	px := make([]leds.RGB, 12)
	px[0] = leds.RGB{R: 255}
	px[1] = leds.RGB{B: 255}
	ch := vkcapsula.RingChannels(px)
	for c, v := range ch {
		want := byte(0)
		if c == 1 || c == 35 {
			want = 255
		}
		if v != want {
			t.Errorf("channel %d = %d, want %d", c, v, want)
		}
	}
	// every colour of every pixel lands on its own channel
	seen := map[int]bool{}
	for i := range 12 {
		for k := range 3 {
			p := make([]leds.RGB, 12)
			switch k {
			case 0:
				p[i].R = 1
			case 1:
				p[i].G = 1
			default:
				p[i].B = 1
			}
			got := vkcapsula.RingChannels(p)
			at := -1
			for c, v := range got {
				if v == 1 {
					if at >= 0 {
						t.Fatalf("pixel %d colour %d lights two channels", i, k)
					}
					at = c
				}
			}
			if at < 0 || seen[at] {
				t.Fatalf("pixel %d colour %d: channel %d missing or reused", i, k, at)
			}
			seen[at] = true
		}
	}
	if len(seen) != 36 {
		t.Fatalf("%d distinct channels", len(seen))
	}
}

func ringTree(t *testing.T) *tree {
	t.Helper()
	tr := capsulaTree(t)
	for c := 1; c <= 36; c++ {
		tr.write("sys/class/leds/top-led"+strconv.Itoa(c)+"/brightness", "9\n")
	}
	return tr
}

func TestRingFallsBackToSysfs(t *testing.T) {
	tr := ringTree(t)
	s := sink(t, tr)
	px := make([]leds.RGB, 12)
	px[0] = leds.RGB{R: 200}
	px[11] = leds.RGB{G: 7}
	if err := s.Ring(px); err != nil {
		t.Fatal(err)
	}
	// pixel 0 red is channel 1, its led top-led2; pixel 11 green is channel 3, top-led4
	for c := 1; c <= 36; c++ {
		want := "0\n"
		switch c {
		case 2:
			want = "200\n"
		case 4:
			want = "7\n"
		}
		if got := readFile(t, tr, "sys/class/leds/top-led"+strconv.Itoa(c)+"/brightness"); got != want {
			t.Errorf("top-led%d = %q, want %q", c, got, want)
		}
	}
	// only changed channels are written again
	tr.write("sys/class/leds/top-led2/brightness", "x\n")
	px[11] = leds.RGB{G: 8}
	if err := s.Ring(px); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, tr, "sys/class/leds/top-led2/brightness"); got != "x\n" {
		t.Errorf("an unchanged channel was written: %q", got)
	}
	if got := readFile(t, tr, "sys/class/leds/top-led4/brightness"); got != "8\n" {
		t.Errorf("top-led4 = %q", got)
	}
}

func TestRingFailureNamesNoValues(t *testing.T) {
	tr := capsulaTree(t)
	err := sink(t, tr).Ring(make([]leds.RGB, 12))
	if err == nil {
		t.Fatal("no error without the leds")
	}
}
