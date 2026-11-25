package otlp

import "time"

// SetNow replaces the clock a Retry-After date is read against.
func SetNow(c *Client, now func() time.Time) { c.now = now }

// SetTimeout shortens the per-request timeout.
func SetTimeout(c *Client, d time.Duration) { c.timeout = d }

// Timeout is the per-request timeout.
func Timeout(c *Client) time.Duration { return c.timeout }
