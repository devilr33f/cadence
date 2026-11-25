package board_test

import (
	"context"
	"errors"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

type fake struct{ id string }

func (f fake) ID() string                          { return f.id }
func (f fake) Fingerprint(context.Context) error   { return nil }
func (f fake) Capabilities() []capability.Provider { return nil }
func (f fake) AudioIO() (audio.IO, bool)           { return audio.IO{}, false }

func TestFind(t *testing.T) {
	boards := []board.Board{fake{"a"}, fake{"sim"}}
	got, err := board.Find(boards, "sim")
	if err != nil || got.ID() != "sim" {
		t.Fatalf("Find(sim) = %v, %v", got, err)
	}
	if _, err := board.Find(boards, "yandex-pickle"); !errors.Is(err, board.ErrUnknownBoard) {
		t.Fatalf("err = %v, want ErrUnknownBoard", err)
	}
}

type lit struct {
	fake
	sink leds.Sink
}

func (l lit) Lights() (leds.Sink, bool) { return l.sink, l.sink != nil }

func TestLightsIsOptional(t *testing.T) {
	var plain board.Board = fake{"a"}
	if _, ok := plain.(board.Lights); ok {
		t.Fatal("a board without lights must not satisfy board.Lights")
	}
	var b board.Board = lit{fake{"b"}, nil}
	l, ok := b.(board.Lights)
	if !ok {
		t.Fatal("board.Lights not found")
	}
	if _, has := l.Lights(); has {
		t.Fatal("a nil sink is no lights")
	}
}
