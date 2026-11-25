package telemetry

import (
	"math"
	"sync"
	"time"
)

// per device, of decompressed bytes (M6b-D8)
const (
	rate  = 1 << 20 // bytes a second
	burst = 8 << 20
)

// bucket is one device's token bucket. A request is let in while the bucket
// holds its compressed size (the burst at most) and charged its decompressed
// size, which can leave the bucket in debt: any request under the caps can
// pass, and the rate holds on average.
type bucket struct {
	mu     sync.Mutex
	tokens float64
	at     time.Time
}

func (b *bucket) refill(now time.Time) {
	switch {
	case b.at.IsZero():
		b.tokens, b.at = burst, now
	case now.After(b.at):
		b.tokens = math.Min(burst, b.tokens+now.Sub(b.at).Seconds()*rate)
		b.at = now
	}
}

// wait is how long n bytes have to wait from now, 0 when they may pass.
func (b *bucket) wait(n int) time.Duration {
	need := math.Min(float64(n), burst)
	if b.tokens >= need {
		return 0
	}
	return time.Duration(math.Ceil((need - b.tokens) / rate * float64(time.Second)))
}

// take takes n bytes now, or reports how long they have to wait and takes nothing.
func (b *bucket) take(now time.Time, n int) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(now)
	if w := b.wait(n); w > 0 {
		return w
	}
	b.tokens -= float64(n)
	return 0
}

// charge takes n bytes now, however few are left: the work they cost was
// done. A negative n gives bytes back, never past the burst.
func (b *bucket) charge(now time.Time, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(now)
	b.tokens = math.Min(burst, b.tokens-float64(n))
}

// settle charges the rest of a request now, as charge does, and reports how
// long the bucket stays in debt if it was in debt before: such a request is
// refused, as one coming in then would have been.
func (b *bucket) settle(now time.Time, rest int) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(now)
	owed := b.tokens < 0
	b.tokens = math.Min(burst, b.tokens-float64(rest))
	if !owed || b.tokens >= 0 {
		return 0
	}
	return time.Duration(math.Ceil(-b.tokens / rate * float64(time.Second)))
}

// retryAfter is a wait in whole seconds for Retry-After, at least 1.
func retryAfter(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}
