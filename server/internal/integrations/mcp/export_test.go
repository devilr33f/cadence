package mcp

import (
	"net/http"
	"time"
)

// Timings shortens a client's waits for a test; a zero field keeps the default.
type Timings struct {
	Connect, List                       time.Duration
	BackoffMin, BackoffMax, StableAfter time.Duration
}

// SetTimings must run before the client connects.
func SetTimings(c *Client, t Timings) {
	set := func(dst *time.Duration, v time.Duration) {
		if v > 0 {
			*dst = v
		}
	}
	set(&c.t.connect, t.Connect)
	set(&c.t.list, t.List)
	set(&c.t.backoffMin, t.BackoffMin)
	set(&c.t.backoffMax, t.BackoffMax)
	set(&c.t.stableAfter, t.StableAfter)
}

// Pin is the transport one of c's sessions sends through.
func Pin(c *Client) http.RoundTripper {
	return newPinned(c.base, c.endpoint, c.auth)
}

var (
	AssignNames = assignNames
	CheckSchema = checkSchema
	Describe    = describe
	ToolHash    = toolHash
	SchemaText  = schemaText
)

// LockListing holds c's listing lock until the func it returns is called.
func LockListing(c *Catalog) func() {
	c.listing <- struct{}{}
	return c.unlockListing
}

// NotSent is whether err failed a request before it left.
var NotSent = notSent

// ToolErrorOf is what the model reads of a failed call; Late is a call whose
// deadline passed before it left.
var ToolErrorOf = toolError

const Late = late
