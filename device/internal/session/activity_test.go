package session

import (
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func waitSnapshot(t *testing.T, a *Activity, want Snapshot) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := a.Snapshot()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot %+v, want %+v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// what a command word needs, read off the controller's goroutine: the session that streams and whether test audio
// speaks in it, and when the player took the last reply frame and whose
func TestActivityFollowsTheSessions(t *testing.T) {
	clock := &stepClock{now: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	a := &Activity{now: clock.Now}
	r := newRig(t, 0, WithActivity(a))
	if got := a.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("at rest %+v", got)
	}

	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Fixture: "weather", Test: true}
	r.next(t)
	waitSnapshot(t, a, Snapshot{Session: "s1", SessionInjected: true})
	heardReply(t, r, "s1", 2)
	clock.advance(1500 * time.Millisecond)
	waitSnapshot(t, a, Snapshot{Session: "s1", SessionInjected: true, Replied: true, SinceReply: 1500 * time.Millisecond, ReplyInjected: true})
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1"})
	waitSnapshot(t, a, Snapshot{Replied: true, SinceReply: 1500 * time.Millisecond, ReplyInjected: true})

	r.woken(t)
	waitSnapshot(t, a, Snapshot{Session: "s2", Replied: true, SinceReply: 1500 * time.Millisecond, ReplyInjected: true})
	heardReply(t, r, "s2", 1)
	waitSnapshot(t, a, Snapshot{Session: "s2", Replied: true})
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	r.next(t)
	clock.advance(time.Second)
	waitSnapshot(t, a, Snapshot{Replied: true, SinceReply: time.Second})
}
