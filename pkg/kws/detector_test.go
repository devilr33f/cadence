package kws

import "testing"

func TestGateNeedsTwoInARowThenRests(t *testing.T) {
	var g gate
	steps := []struct {
		score float32
		hit   bool
	}{{0.9, false}, {0.1, false}, {0.9, false}, {0.95, true}, {0.99, false}}
	for i, s := range steps {
		if got := g.step(s.score, 0.8); got != s.hit {
			t.Fatalf("step %d: hit %v", i, got)
		}
	}
	for range Refractory - 2 {
		if g.step(1, 0.8) {
			t.Fatal("a hit inside the refractory period")
		}
	}
	if g.step(1, 0.8) {
		t.Fatal("a hit on the period's last step")
	}
	if g.step(1, 0.8) {
		t.Fatal("a hit on a single window")
	}
	if !g.step(1, 0.8) {
		t.Fatal("no hit after the refractory period")
	}
}

func TestThresholdFollowsSensitivity(t *testing.T) {
	m := &Model{Threshold: 0.8}
	if Threshold(m, DefaultSensitivity) != 0.8 {
		t.Fatal("the default sensitivity moved the threshold")
	}
	if !(Threshold(m, 1) < 0.8 && Threshold(m, 0) > 0.8) || Threshold(m, 0) > 0.99 {
		t.Fatal("sensitivity runs the wrong way")
	}
}
