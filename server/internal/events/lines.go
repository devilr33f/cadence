package events

import (
	"sync"
	"time"
)

const (
	// lineRate is how many log lines a peer gets a second (M6b-D10)
	lineRate = 50
	// spanRate is how many span events a peer gets a second
	spanRate = 200
	// lineEvery is how often a peer hears how many lines it missed
	lineEvery = time.Second
)

// lineLimit passes a peer's log lines (or span events), at most rate (zero:
// lineRate) in each second of now; it counts the rest until the pump reports
// them.
type lineLimit struct {
	now  func() time.Time
	rate int

	mu      sync.Mutex
	window  time.Time
	passed  int
	dropped uint64
}

func (l *lineLimit) allow() bool {
	at := l.now().Truncate(time.Second)
	l.mu.Lock()
	defer l.mu.Unlock()
	if !at.Equal(l.window) {
		l.window, l.passed = at, 0
	}
	rate := l.rate
	if rate == 0 {
		rate = lineRate
	}
	if l.passed < rate {
		l.passed++
		return true
	}
	l.dropped++
	return false
}

// takeDropped returns the lines dropped since the last call.
func (l *lineLimit) takeDropped() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.dropped
	l.dropped = 0
	return n
}
