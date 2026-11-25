//go:build cadence_notrace

package tracing

import (
	"context"
	"time"
)

// Linked reports whether this build records spans: false with cadence_notrace.
const Linked = false

// Provider is the notrace build's: it records and ships nothing, and the sdk is not linked.
type Provider struct{}

// Stats is always zero in this build.
type Stats struct{ Sent, Rejected, Dropped uint64 }

// Poster is as the tracing build's, so callers build either way.
type Poster interface {
	PostEncoded(ctx context.Context, path string, body []byte) (status int, retryAfter time.Duration, err error)
}

func Setup(Poster, string) *Provider             { return &Provider{} }
func (*Provider) Run(context.Context) func()     { return func() {} }
func (*Provider) Shutdown(context.Context) error { return nil }
func (*Provider) Stats() Stats                   { return Stats{} }
