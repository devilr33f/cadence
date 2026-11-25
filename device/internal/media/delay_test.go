package media_test

import (
	"testing"
)

// audio.output's delay trim reaches audiod live, bounded, and again after every Ready; none is sent before one is set
func TestOutputDelayGoesToAudiodAndAgainAfterReady(t *testing.T) {
	r := newRig(t)
	r.up(t)
	r.none(t)
	r.m.SetOutputDelay(-40)
	if d := r.next(t).GetOutputDelay(); d == nil || d.GetDelayMs() != -40 {
		t.Fatalf("delay = %v", d)
	}
	r.m.SetOutputDelay(5000)
	if d := r.next(t).GetOutputDelay(); d.GetDelayMs() != 1000 {
		t.Fatalf("delay = %v, want the bound", d)
	}
	r.m.MediaGone()
	r.ready()
	r.next(t)
	if d := r.next(t).GetOutputDelay(); d.GetDelayMs() != 1000 {
		t.Fatalf("delay not sent again: %v", d)
	}
}
