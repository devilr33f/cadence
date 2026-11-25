package link

import "time"

const (
	firstDelay = time.Second
	maxDelay   = 60 * time.Second
	// a link that stayed up this long was healthy: the next failure starts over at firstDelay
	resetAfter = 60 * time.Second
)

// Delay is the wait before reconnect attempt n (from 1): 1 s doubling to 60 s, with ±20 % jitter from rnd in [0, 1).
func Delay(attempt int, rnd func() float64) time.Duration {
	d := firstDelay
	for i := 1; i < attempt && d < maxDelay; i++ {
		d *= 2
	}
	d = min(d, maxDelay)
	return time.Duration(float64(d) * (0.8 + 0.4*rnd()))
}

// nextAttempt numbers the attempt after a link that was up for up.
func nextAttempt(attempt int, up time.Duration) int {
	if up >= resetAfter {
		return 1
	}
	return attempt + 1
}
