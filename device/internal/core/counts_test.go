package core_test

import (
	"slices"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type sessionCounts struct {
	mu  sync.Mutex
	got []cadencev2.Trigger
}

func (c *sessionCounts) SessionBegan(t cadencev2.Trigger) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, t)
}

func (c *sessionCounts) began() []cadencev2.Trigger {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

// every link's controller counts its sessions into the one place core is given
func TestSessionsCounted(t *testing.T) {
	triggers := make(chan audio.Trigger, 1)
	b := fakeBoard{id: "fake", aio: &audio.IO{Triggers: triggers, Mic: chanMic{}}}
	counts := &sessionCounts{}
	ls, buf := startSync(t, b, openStore(t), core.WithCounts(counts))
	p := ls.accept(t)
	p.handshake(false)
	waitLog(t, buf, "link_up")

	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	start := p.recv().GetSessionStart()
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{SessionId: start.GetSessionId()}}})
	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	p.recv()
	want := []cadencev2.Trigger{cadencev2.Trigger_TRIGGER_BUTTON, cadencev2.Trigger_TRIGGER_WAKE}
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Equal(counts.began(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("counted %v", counts.began())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
