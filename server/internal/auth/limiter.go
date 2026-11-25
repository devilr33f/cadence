package auth

import (
	"slices"
	"sync"
	"time"
)

const (
	// LoginWindow is how long a failed login counts: at most
	// LoginFailuresPerIP from one client ip (or one known browser) and
	// LoginFailures in all from clients without a known browser.
	LoginWindow        = 15 * time.Minute
	LoginFailuresPerIP = 5
	LoginFailures      = 20
	globalBucket       = "global"
)

// try is one login attempt, counted in its bucket and, from a client without a
// known browser, in the global one.
type try struct {
	key    string
	at     time.Time
	global bool
}

type bucket struct {
	tries []*try
	// refusals since the bucket filled
	refused int
}

// limiter counts failed logins in memory (a restart forgets them), per bucket:
// "ip:<address or /64>" or "browser:<id>", and globally for the ip ones. An
// attempt counts as a failure from its start, so parallel guesses cannot pass
// the limit; a success takes its own back and clears its bucket. An ip bucket's
// tries hold global slots, so at most LoginFailures of them are tracked; a
// browser bucket exists only for a token a login issued.
type limiter struct {
	mu   sync.Mutex
	now  func() time.Time
	keys map[string]*bucket
	all  bucket
	// report is told a bucket's refusals once its window ends, under the lock
	report func(key string, count int)
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, keys: map[string]*bucket{}, report: func(string, int) {}}
}

// refusal is a refused attempt: the bucket that refused it (the global one
// when only it is full), the wait until a slot frees, and whether it is that
// bucket's first refusal of its window.
type refusal struct {
	key   string
	wait  time.Duration
	first bool
}

// begin holds an attempt in key's bucket, and in the global one when global,
// or refuses it.
func (l *limiter) begin(key string, global bool) (*try, refusal, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepAt(now)
	b := l.keys[key]
	var r refusal
	var by *bucket
	if b != nil && len(b.tries) >= LoginFailuresPerIP {
		r, by = refusal{key: key, wait: b.tries[len(b.tries)-LoginFailuresPerIP].at.Add(LoginWindow).Sub(now)}, b
	}
	if n := len(l.all.tries); global && n >= LoginFailures {
		if by == nil {
			r.key, by = globalBucket, &l.all
		}
		r.wait = max(r.wait, l.all.tries[n-LoginFailures].at.Add(LoginWindow).Sub(now))
	}
	if by != nil {
		by.refused++
		r.first = by.refused == 1
		return nil, r, false
	}
	if b == nil {
		b = &bucket{}
		l.keys[key] = b
	}
	t := &try{key: key, at: now, global: global}
	b.tries = append(b.tries, t)
	if global {
		l.all.tries = append(l.all.tries, t)
	}
	return t, refusal{}, true
}

// succeeded clears t's bucket and frees its global slot; the bucket's earlier
// failures stay in the global count.
func (l *limiter) succeeded(t *try) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.keys[t.key]; b != nil && b.refused > 0 {
		l.report(t.key, b.refused)
	}
	delete(l.keys, t.key)
	l.all.tries = slices.DeleteFunc(l.all.tries, func(u *try) bool { return u == t })
}

// abandon takes back an attempt that checked no password.
func (l *limiter) abandon(t *try) {
	l.mu.Lock()
	defer l.mu.Unlock()
	drop := func(u *try) bool { return u == t }
	if b := l.keys[t.key]; b != nil {
		b.tries = slices.DeleteFunc(b.tries, drop)
	}
	l.all.tries = slices.DeleteFunc(l.all.tries, drop)
}

// sweep ends the windows that ran out, reporting their refusals.
func (l *limiter) sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepAt(l.now())
}

func (l *limiter) sweepAt(now time.Time) {
	old := func(t *try) bool { return !t.at.Add(LoginWindow).After(now) }
	l.all.tries = slices.DeleteFunc(l.all.tries, old)
	if l.all.refused > 0 && len(l.all.tries) < LoginFailures {
		l.report(globalBucket, l.all.refused)
		l.all.refused = 0
	}
	for key, b := range l.keys {
		b.tries = slices.DeleteFunc(b.tries, old)
		if b.refused > 0 && len(b.tries) < LoginFailuresPerIP {
			l.report(key, b.refused)
			b.refused = 0
		}
		if len(b.tries) == 0 && b.refused == 0 {
			delete(l.keys, key)
		}
	}
}
