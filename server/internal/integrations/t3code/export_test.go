package t3code

import (
	"encoding/json"
	"time"
)

// FastTimings shortens backoff and pings for tests.
func FastTimings(c *Client, backoff, ping time.Duration) {
	c.t.backoffMin, c.t.backoffMax = backoff, backoff
	c.t.pingEvery, c.t.pongWait = ping, ping
	c.t.call, c.t.handshake = 5*time.Second, 5*time.Second
}

func RequestFrame(id int64, method string, payload json.RawMessage) Frame {
	return requestFrame(id, method, payload)
}
func AckFrame(id int64) Frame       { return ackFrame(id) }
func InterruptFrame(id int64) Frame { return interruptFrame(id) }

func ExitError(e *ExitValue) error { return exitError(e) }
