package session

import (
	"slices"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
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

func (c *sessionCounts) wait(t *testing.T, want ...cadencev2.Trigger) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Equal(c.began(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("counted %v, want %v", c.began(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// each session counts once by what began it: a wake, a press, a follow-up; a wake a session ignores and a press that
// cancels begin none
func TestSessionsCountedByTrigger(t *testing.T) {
	counts := &sessionCounts{}
	r := newRig(t, 0, WithCounts(counts))
	r.woken(t)
	heardReply(t, r, "s1", 3)
	r.handle(t, followUpClose("s1"))
	if s := r.next(t).GetSessionStart(); s.GetTrigger() != cadencev2.Trigger_TRIGGER_FOLLOW_UP {
		t.Fatalf("start = %v", s)
	}
	counts.wait(t, cadencev2.Trigger_TRIGGER_WAKE, cadencev2.Trigger_TRIGGER_FOLLOW_UP)

	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	if c := r.next(t).GetSessionCancel(); c.GetSessionId() != "s2" {
		t.Fatalf("cancel = %v", c)
	}
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	if s := r.next(t).GetSessionStart(); s.GetSessionId() != "s3" {
		t.Fatalf("start = %v", s)
	}
	counts.wait(t, cadencev2.Trigger_TRIGGER_WAKE, cadencev2.Trigger_TRIGGER_FOLLOW_UP, cadencev2.Trigger_TRIGGER_BUTTON)
}
