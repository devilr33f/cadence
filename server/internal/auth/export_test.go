package auth

import "time"

// test seams: the verification an owner check runs, the hash it runs against
// when no password is set, cheaper params for a test owner, and the limiter

func SetVerify(o *Owner, f func(encoded, password string) (bool, error)) { o.verify = f }

func SetParams(o *Owner, p Params) { o.params = p }

var DummyHash = dummyHash

func DecodeParams(s string) (Params, bool) { return decodeParams(s) }

func BucketKey(ip string) string { return bucketKey(ip) }

type (
	Limiter = limiter
	Try     = try
)

func NewLimiterAt(now func() time.Time, report func(key string, count int)) *Limiter {
	l := newLimiter(now)
	l.report = report
	return l
}

func (l *limiter) Begin(key string, global bool) (*try, time.Duration, bool, bool) {
	t, r, ok := l.begin(key, global)
	return t, r.wait, r.first, ok
}

func (l *limiter) Succeeded(t *try) { l.succeeded(t) }

func (l *limiter) Abandon(t *try) { l.abandon(t) }

func (l *limiter) Sweep() { l.sweep() }
