package telemetry

import (
	"sync/atomic"

	"github.com/gofiber/fiber/v3"
)

// failCodes are every code a refused request is counted under; anything else is other
var failCodes = []string{
	"auth", "content_type", "content_encoding", "too_large", "gzip", "decode", "secrets", "store", "encode", "device", "other",
}

// signalCounts are one signal's ingest counts since the server started.
type signalCounts struct {
	// records or points stored, and rejected by partial success
	accepted, rejected atomic.Uint64
	// requests answered 429, and 503 for want of a decompression slot
	rateLimited, busy atomic.Uint64
	// requests refused for another reason, by code; the map is never written after newSignalCounts
	failed map[string]*atomic.Uint64
}

func newSignalCounts() *signalCounts {
	c := &signalCounts{failed: make(map[string]*atomic.Uint64, len(failCodes))}
	for _, code := range failCodes {
		c.failed[code] = new(atomic.Uint64)
	}
	return c
}

func (c *signalCounts) count(o outcome) {
	switch {
	case o.status == fiber.StatusTooManyRequests:
		c.rateLimited.Add(1)
	case o.code == "busy":
		c.busy.Add(1)
	case o.code != "":
		c.fail(o.code)
	default:
		c.accepted.Add(uint64(o.stored))   //nolint:gosec // a count
		c.rejected.Add(uint64(o.rejected)) //nolint:gosec // a count
	}
}

func (c *signalCounts) fail(code string) {
	n, ok := c.failed[code]
	if !ok {
		n = c.failed["other"]
	}
	n.Add(1)
}

// SignalStats are one signal's ingest counts: records or points accepted and
// rejected, requests rate limited, busy, and refused by code.
type SignalStats struct {
	Accepted, Rejected, RateLimited, Busy uint64
	Failed                                map[string]uint64
}

func (c *signalCounts) stats() SignalStats {
	s := SignalStats{Accepted: c.accepted.Load(), Rejected: c.rejected.Load(), RateLimited: c.rateLimited.Load(),
		Busy: c.busy.Load(), Failed: make(map[string]uint64, len(c.failed))}
	for code, n := range c.failed {
		s.Failed[code] = n.Load()
	}
	return s
}

// Stats are the ingest's counts of each signal since the server started.
func (in *Ingest) Stats() (logs, metrics SignalStats) {
	return in.counts[kindLogs].stats(), in.counts[kindMetrics].stats()
}
