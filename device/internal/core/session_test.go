package core_test

import (
	"context"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/session"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type chanMic struct{}

func (chanMic) Open(ctx context.Context, _ audio.Trigger) (<-chan []byte, error) {
	ch := make(chan []byte, 1)
	ch <- []byte{7}
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}

func TestSessionRunsOverTheLink(t *testing.T) {
	triggers := make(chan audio.Trigger, 1)
	b := fakeBoard{id: "fake", aio: &audio.IO{Triggers: triggers, Mic: chanMic{}}}
	ls, buf := startSync(t, b, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	// a trigger before the link is up is refused
	waitLog(t, buf, "link_up")

	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	start := p.recv().GetSessionStart()
	if start == nil || start.GetSessionId() == "" || start.GetTrigger() != cadencev2.Trigger_TRIGGER_BUTTON {
		t.Fatalf("start = %v", start)
	}
	if m := p.recv().GetMicAudio(); m == nil || m.GetSessionId() != start.GetSessionId() || m.GetSeq() != 0 {
		t.Fatalf("mic = %v", m)
	}

	// a server frame reaches the controller: the close ends the session, so the next trigger starts a new one
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{SessionId: start.GetSessionId()}}})
	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	if s2 := p.recv().GetSessionStart(); s2 == nil || s2.GetSessionId() == start.GetSessionId() {
		t.Fatalf("second start = %v", s2)
	}
}

// trackingBoard keeps how each session began, as a board with volume does
type trackingBoard struct {
	fakeBoard
	recent *session.Recent
}

func (b trackingBoard) Sessions() *session.Recent { return b.recent }

func TestSessionsRecordedOnTheBoard(t *testing.T) {
	triggers := make(chan audio.Trigger, 1)
	b := trackingBoard{fakeBoard: fakeBoard{id: "fake", aio: &audio.IO{Triggers: triggers, Mic: chanMic{}}}, recent: &session.Recent{}}
	ls, buf := startSync(t, b, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	// a trigger before the link is up is refused
	waitLog(t, buf, "link_up")

	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	start := p.recv().GetSessionStart()
	if got, ok := b.recent.Trigger(start.GetSessionId()); !ok || got != cadencev2.Trigger_TRIGGER_WAKE {
		t.Fatalf("recorded %v, %v", got, ok)
	}
}
