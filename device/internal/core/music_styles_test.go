package core

import (
	"context"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
	"git.shino.boo/devilreef/cadence/device/internal/media"
)

type musicSink struct{ styles chan leds.MusicStyle }

func (s musicSink) SetMusic(_ [leds.MusicBandCount]uint8, style leds.MusicStyle) { s.styles <- style }

// every style media.viz takes reaches the lights as its own style, never as off
func TestShowMusicPassesEveryStyle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan media.VizFrame)
	sink := musicSink{styles: make(chan leds.MusicStyle, 1)}
	go showMusic(ctx, frames, sink)
	want := map[string]leds.MusicStyle{
		"bands": leds.MusicBands, "pulse": leds.MusicPulse, "spectrum": leds.MusicSpectrum,
		"equalizer": leds.MusicEqualizer, "orbit": leds.MusicOrbit, "beat": leds.MusicBeat,
	}
	for name, style := range want {
		frames <- media.VizFrame{Style: name}
		select {
		case got := <-sink.styles:
			if got != style {
				t.Fatalf("%s showed as %d", name, got)
			}
		case <-time.After(time.Second):
			t.Fatal("no frame reached the lights")
		}
	}
}
