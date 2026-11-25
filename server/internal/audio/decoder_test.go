//go:build cgo

package audio

import (
	"errors"
	"math"
	"testing"
)

// a 48 kHz packet decodes at 24 kHz, the rate the voice pipeline asks tts for
func TestDecodeAtAnotherRate(t *testing.T) {
	enc, err := NewEncoder(EncoderOptions{SampleRate: 48000, Channels: 1, Bitrate: 64000, Complexity: 5, Application: AppAudio})
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewDecoder(24000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, enc.FrameSamples())
	var energy float64
	for frame := range 5 {
		for i := range pcm {
			pcm[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(frame*len(pcm)+i)/48000))
		}
		pkt, err := enc.Encode(pcm)
		if err != nil {
			t.Fatal(err)
		}
		out, err := dec.Decode(pkt)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 480 {
			t.Fatalf("decoded %d samples, want 480 (20 ms at 24 kHz)", len(out))
		}
		for _, s := range out {
			energy += float64(s) * float64(s)
		}
	}
	if energy == 0 {
		t.Fatal("decoded silence")
	}
}

func TestDecodeRefusesGarbage(t *testing.T) {
	dec, err := NewDecoder(24000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dec.Decode(nil); !errors.Is(err, ErrDecode) {
		t.Fatalf("empty packet: %v", err)
	}
	if _, err := NewDecoder(44100, 1); !errors.Is(err, ErrOptions) {
		t.Fatalf("44.1 kHz: %v", err)
	}
}
