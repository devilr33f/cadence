package mcp

import "time"

// Clock is the catalog's time: now, and a timer that runs f on its own
// goroutine once d has passed.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

type Timer interface{ Stop() bool }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
