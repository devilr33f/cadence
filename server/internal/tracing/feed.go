package tracing

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// FeedData is a traces topic event's data: the store's sequence number of
// the change and the whole span. The event's type is span_start, span_update
// or span_end, its deviceId the span's device (resource or device.id).
type FeedData struct {
	Seq  uint64   `json:"seq"`
	Span SpanView `json:"span"`
}

// PublishTo forwards the store's span events onto bus as the traces topic's
// while wanted says a feed subscribed to it (the topic is opt-in, so mostly
// none did and the events are only drained). stop ends it.
func (p *Provider) PublishTo(ctx context.Context, bus events.Publisher, wanted func() bool) (stop func()) {
	ch, cancel := p.store.Subscribe()
	ctx, end := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if !wanted() {
					continue
				}
				bus.Publish(events.Event{Type: ev.Type, DeviceID: spanDevice(&ev.Span), At: ev.Span.Start,
					Data: FeedData{Seq: ev.Seq, Span: ev.Span}})
			}
		}
	}()
	return func() {
		end()
		cancel()
		<-done
	}
}
