//go:build !cadence_notrace

package tracing

import (
	"context"

	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Linked reports whether this build records spans: false with cadence_notrace.
const Linked = true

// span limits: a device span is small, and the server's guard cuts again
func limits() sdktrace.SpanLimits {
	l := sdktrace.NewSpanLimits()
	l.AttributeValueLengthLimit, l.AttributeCountLimit, l.EventCountLimit, l.LinkCountLimit = 256, 16, 32, 4
	l.AttributePerEventCountLimit, l.AttributePerLinkCountLimit = 8, 0
	return l
}

// Provider records spans and ships them to the server.
type Provider struct {
	tp   *sdktrace.TracerProvider
	ship *shipper
}

// Stats counts the spans the server took, those it refused, and those the
// full ring dropped before they went.
type Stats struct{ Sent, Rejected, Dropped uint64 }

// Setup records spans from now on and keeps them for client (the logs'
// client: the device token, verified tls), which Run posts them through. A
// nil client records nothing.
func Setup(client Poster, version string) *Provider {
	if client == nil {
		return &Provider{}
	}
	s := newShipper(client, version)
	tp := sdktrace.NewTracerProvider(
		// a device span is the root of a turn or a child of a sampled caller's: every one is kept, as turns are few
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithRawSpanLimits(limits()),
		// the resource goes out as the shipper writes it; the sdk's detectors read env and host for nothing
		sdktrace.WithResource(resource.Empty()),
		sdktrace.WithSpanProcessor(s),
	)
	current.Store(&tracerBox{t: tp.Tracer(ScopeName)})
	return &Provider{tp: tp, ship: s}
}

// Run ships the spans until ctx ends; stop waits for it.
func (p *Provider) Run(ctx context.Context) (stop func()) {
	if p.ship == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.ship.run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// Shutdown stops recording: the spans still open are not shipped.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p.tp == nil {
		return nil
	}
	current.Store(nil)
	return p.tp.Shutdown(ctx)
}

func (p *Provider) Stats() Stats {
	if p.ship == nil {
		return Stats{}
	}
	return Stats{Sent: p.ship.sent.Load(), Rejected: p.ship.rejected.Load(), Dropped: p.ship.ring.Dropped()}
}
