package wire_test

import (
	"errors"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

func TestEncodeRejectsLargeFrame(t *testing.T) {
	env := &cadencev2.Envelope{Body: &cadencev2.Envelope_AudioDown{
		AudioDown: &cadencev2.AudioDown{StreamId: "s", Opus: make([]byte, wire.MaxFrameBytes+1)},
	}}
	if _, err := wire.Encode(env); !errors.Is(err, wire.ErrFrameTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := wire.Decode([]byte{0xff, 0xff}); !errors.Is(err, wire.ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}

// a newer peer's message type parses with no body this side knows
func TestDecodeUnknownBody(t *testing.T) {
	empty, err := proto.Marshal(&cadencev2.Envelope{})
	if err != nil {
		t.Fatal(err)
	}
	newer := protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1)
	for name, b := range map[string][]byte{"empty": empty, "newer type": newer} {
		if _, err := wire.Decode(b); !errors.Is(err, wire.ErrUnknown) || errors.Is(err, wire.ErrInvalid) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
}

func TestDecodeRejectsLargeFrame(t *testing.T) {
	if _, err := wire.Decode(make([]byte, wire.MaxFrameBytes+1)); !errors.Is(err, wire.ErrFrameTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	env := &cadencev2.Envelope{Body: &cadencev2.Envelope_Hello{
		Hello: &cadencev2.Hello{Protocol: 2, Board: "sim"},
	}}
	b, err := wire.Encode(env)
	if err != nil {
		t.Fatal(err)
	}
	got, err := wire.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, env) {
		t.Fatalf("got %v", got)
	}
}
