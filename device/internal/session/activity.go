package session

import (
	"sync"
	"time"
)

// Activity is what the sessions do now, for a command word heard off the controller's goroutine (M6a-D6): the session
// that streams the microphone, and when the player last took a reply frame. One serves each link's controller in
// turn. The zero value is ready.
type Activity struct {
	// defaults to time.Now
	now func() time.Time

	mu       sync.Mutex
	session  string
	injected bool
	// the last reply frame the player took, and whether test audio spoke its session
	replyAt       time.Time
	replyInjected bool
}

// Snapshot is the activity at one moment.
type Snapshot struct {
	// the session that streams the microphone, "" for none, and whether test audio (the sim's say file) speaks in it
	Session         string
	SessionInjected bool
	// the player took a reply frame, SinceReply ago, in a session test audio spoke in or not
	Replied       bool
	SinceReply    time.Duration
	ReplyInjected bool
}

// Snapshot is the activity now.
func (a *Activity) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := Snapshot{Session: a.session, SessionInjected: a.injected}
	if !a.replyAt.IsZero() {
		s.Replied, s.SinceReply, s.ReplyInjected = true, a.clock().Sub(a.replyAt), a.replyInjected
	}
	return s
}

func (a *Activity) clock() time.Time {
	if a.now == nil {
		return time.Now()
	}
	return a.now()
}

func (a *Activity) began(id string, injected bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session, a.injected = id, injected
}

func (a *Activity) ended(id string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == id {
		a.session, a.injected = "", false
	}
}

func (a *Activity) replyFrame(injected bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.replyAt, a.replyInjected = a.clock(), injected
}
