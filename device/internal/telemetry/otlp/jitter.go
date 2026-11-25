package otlp

import "time"

// a wait is spread by this share of itself, so devices that failed together do
// not all retry together, as the link's backoff is
const spread = 0.2

// Jitter spreads a wait the device chose (a backoff) over ±20 %, by rnd in [0, 1).
func Jitter(d time.Duration, rnd func() float64) time.Duration {
	return time.Duration(float64(d) * (1 - spread + 2*spread*rnd()))
}

// Stretch spreads a wait the server asked for (Retry-After) over 0 to +20 %, by
// rnd in [0, 1): never shorter than asked.
func Stretch(d time.Duration, rnd func() float64) time.Duration {
	return time.Duration(float64(d) * (1 + spread*rnd()))
}
