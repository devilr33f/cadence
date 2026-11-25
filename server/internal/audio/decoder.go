package audio

import "errors"

// Decoder turns Opus packets into PCM; not safe for concurrent use. Its prediction state belongs to one stream: a
// new logical stream needs a new decoder.
type Decoder interface {
	// Decode returns the packet's samples, channels interleaved, at the decoder's rate.
	Decode(packet []byte) ([]int16, error)
}

var ErrDecode = errors.New("audio: opus decode failed")

// NewDecoder decodes any Opus packet to sampleRate (8, 12, 16, 24 or 48 kHz), whatever rate it was encoded at.
func NewDecoder(sampleRate, channels int) (Decoder, error) {
	switch sampleRate {
	case 8000, 12000, 16000, 24000, 48000:
	default:
		return nil, ErrOptions
	}
	if channels != 1 && channels != 2 {
		return nil, ErrOptions
	}
	return newOpusDecoder(sampleRate, channels)
}
