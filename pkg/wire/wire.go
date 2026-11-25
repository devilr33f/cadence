// Package wire frames cadence.v2 envelopes for the device link.
package wire

import (
	"errors"

	"google.golang.org/protobuf/proto"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// MaxFrameBytes bounds one envelope on the device link. websocket frames and reassembles a message of any size, so
// this is a sanity cap against a broken peer, never a size replies are fitted into
const MaxFrameBytes = 16 << 20

// MaxAudiodFrameBytes bounds one frame on cadenced's socket to audiod, whose c++ side reads into a buffer this size
const MaxAudiodFrameBytes = 16384

var (
	ErrFrameTooLarge = errors.New("wire: frame too large")
	ErrInvalid       = errors.New("wire: invalid frame")
	// ErrUnknown: the frame parses but carries no body this side knows, such as a
	// newer peer's message type; a link skips it rather than closing.
	ErrUnknown = errors.New("wire: unknown message")
)

func Encode(env *cadencev2.Envelope) ([]byte, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(env)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(b) > MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}
	return b, nil
}

func Decode(b []byte) (*cadencev2.Envelope, error) {
	if len(b) > MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}
	env := &cadencev2.Envelope{}
	if err := proto.Unmarshal(b, env); err != nil {
		return nil, ErrInvalid
	}
	if env.GetBody() == nil {
		return nil, ErrUnknown
	}
	return env, nil
}
