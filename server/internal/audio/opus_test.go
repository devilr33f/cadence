//go:build cgo

package audio

import (
	"errors"
	"math"
	"testing"
)

func newReplyEncoder(t *testing.T) Encoder {
	t.Helper()
	enc, err := NewEncoder(EncoderOptions{SampleRate: 24000, Channels: 1, Bitrate: 64000, Complexity: 10, Application: AppAudio})
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestEncodeFrame(t *testing.T) {
	enc := newReplyEncoder(t)
	if enc.FrameSamples() != 480 {
		t.Fatalf("frame samples %d", enc.FrameSamples())
	}
	pcm := make([]int16, 480)
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/24000))
	}
	pkt, err := enc.Encode(pcm)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt) == 0 {
		t.Fatal("empty packet")
	}
	if n := packetSamples(pkt, 24000); n != 480 {
		t.Fatalf("libopus reports %d samples, want 480 (20 ms)", n)
	}
}

func TestRejectsWrongFrameSize(t *testing.T) {
	enc := newReplyEncoder(t)
	for _, n := range []int{0, 479, 481, 960} {
		if _, err := enc.Encode(make([]int16, n)); !errors.Is(err, ErrFrameSize) {
			t.Fatalf("%d samples: err %v", n, err)
		}
	}
}
