package ha

import "time"

// Timings shortens a client's waits for a test; a zero field keeps the default.
type Timings struct {
	BackoffMin, BackoffMax, StableAfter time.Duration
	PingEvery, PongWait                 time.Duration
	Command                             time.Duration
	ReadLimit                           int64
}

// SetTimings must run before Run.
func SetTimings(c *Client, t Timings) {
	set := func(dst *time.Duration, v time.Duration) {
		if v > 0 {
			*dst = v
		}
	}
	set(&c.t.backoffMin, t.BackoffMin)
	set(&c.t.backoffMax, t.BackoffMax)
	set(&c.t.stableAfter, t.StableAfter)
	set(&c.t.pingEvery, t.PingEvery)
	set(&c.t.pongWait, t.PongWait)
	set(&c.t.command, t.Command)
	if t.ReadLimit > 0 {
		c.t.readLimit = t.ReadLimit
	}
}

// URL is the WebSocket URL the client dials.
func URL(c *Client) string { return c.url }

// SetQuiet sets how long h's re-list worker waits after a kick; call it
// before the client runs.
func SetQuiet(h *Home, d time.Duration) { h.quiet = d }

// SetRetry sets the backoff of h's failed re-lists; call it before the
// client runs.
func SetRetry(h *Home, first, most time.Duration) { h.retryFirst, h.retryMost = first, most }

// RetryDelays are the waits before each of n failed re-lists in a row is
// tried again.
func RetryDelays(h *Home, n int) []time.Duration {
	var out []time.Duration
	d := time.Duration(0)
	for range n {
		d = h.nextRetry(d)
		out = append(out, d)
	}
	return out
}

// VersionToken is the version the status shows of what Home Assistant reported.
func VersionToken(v string) string { return versionToken(v) }
