// Package audio encodes the server's PCM into 20 ms Opus packets, and decodes
// the Opus a speech provider answers in. Both need cgo and libopus; a build
// without cgo returns ErrUnavailable.
package audio

import "errors"

type Application int

const (
	AppAudio Application = iota + 1
	AppVoIP
)

type EncoderOptions struct {
	SampleRate, Channels, Bitrate, Complexity int
	Application                               Application
}

// Encoder is not safe for concurrent use.
type Encoder interface {
	// Encode takes exactly one frame, channels interleaved.
	Encode(pcm []int16) ([]byte, error)
	// FrameSamples is the int16 count of one 20 ms frame over all channels.
	FrameSamples() int
}

// the bitrates opus defines (RFC 6716), in bits per second
const (
	MinBitrate = 6000
	MaxBitrate = 510000
)

var (
	ErrUnavailable = errors.New("audio: opus not in this build")
	ErrOptions     = errors.New("audio: invalid encoder options")
	ErrFrameSize   = errors.New("audio: pcm is not one 20 ms frame")
)

func NewEncoder(o EncoderOptions) (Encoder, error) {
	if !valid(o) {
		return nil, ErrOptions
	}
	return newOpus(o)
}

func valid(o EncoderOptions) bool {
	switch o.SampleRate {
	case 8000, 12000, 16000, 24000, 48000:
	default:
		return false
	}
	return (o.Channels == 1 || o.Channels == 2) &&
		o.Bitrate >= MinBitrate && o.Bitrate <= MaxBitrate &&
		o.Complexity >= 0 && o.Complexity <= 10 &&
		(o.Application == AppAudio || o.Application == AppVoIP)
}
