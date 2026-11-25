//go:build !cadence_notrace

package tracing

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
)

const (
	// the finished spans kept for the server, encoded; a full ring drops the
	// oldest. a span is some hundred bytes, a turn some ten spans
	ringBytes = 256 << 10
	// a batch goes out every batchEvery, or as soon as batchWaiting bytes wait
	batchEvery   = 5 * time.Second
	batchWaiting = 64 << 10
	// one request's spans at most, below the server's 2048
	maxBatch     = 512
	firstBackoff = time.Second
	maxBackoff   = 60 * time.Second
)

// Poster sends one encoded OTLP request; *otlp.Client is one.
type Poster interface {
	PostEncoded(ctx context.Context, path string, body []byte) (status int, retryAfter time.Duration, err error)
}

// shipper keeps finished spans in a bounded ring (logs.Ring: drop-oldest,
// counted) and posts them in batches; it is the sdk's span processor.
type shipper struct {
	client   Poster
	resource []byte
	ring     *logs.Ring
	wake     chan struct{}
	rnd      func() float64
	after    func(time.Duration) <-chan time.Time
	rejected atomic.Uint64
	sent     atomic.Uint64
	backoff  time.Duration
}

func newShipper(client Poster, version string) *shipper {
	return &shipper{
		client: client, resource: otlp.TraceResource(version), ring: logs.NewRing(ringBytes),
		wake: make(chan struct{}, 1), rnd: rand.Float64, after: time.After,
	}
}

func (*shipper) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

// OnEnd encodes a sampled span into the ring; it never waits on the network.
func (s *shipper) OnEnd(span sdktrace.ReadOnlySpan) {
	if !span.SpanContext().IsSampled() {
		return
	}
	b, err := proto.Marshal(encodeSpan(span))
	if err != nil {
		return
	}
	if s.ring.Push(b) >= batchWaiting {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (*shipper) Shutdown(context.Context) error   { return nil }
func (*shipper) ForceFlush(context.Context) error { return nil }

// run ships batches until ctx ends.
func (s *shipper) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.after(batchEvery):
		case <-s.wake:
		}
		if !s.flush(ctx) {
			return
		}
	}
}

// flush sends what waits, oldest first, until less than a batch's worth is
// left; false once ctx ended.
func (s *shipper) flush(ctx context.Context) bool {
	for {
		recs := s.ring.Peek(ringBytes)
		if len(recs) == 0 {
			return true
		}
		recs = recs[:min(len(recs), maxBatch)]
		sent, wait := s.send(ctx, recs)
		if wait > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-s.after(wait):
			}
			continue
		}
		if sent == len(recs) && s.ring.Bytes() < batchWaiting {
			return true
		}
	}
}

// send posts the oldest of recs, halving them until a request fits the
// compressed cap, and returns how many are done with, or how long to wait.
func (s *shipper) send(ctx context.Context, recs [][]byte) (int, time.Duration) {
	n := len(recs)
	for {
		status, retryAfter, err := s.client.PostEncoded(ctx, otlp.TracesPath, otlp.TracesRequest(s.resource, recs[:n]))
		switch {
		case errors.Is(err, otlp.ErrTooLarge) && n > 1:
			n /= 2
			continue
		case errors.Is(err, otlp.ErrTooLarge):
			return s.refuse(1), 0
		case err != nil:
			return 0, s.fail()
		case status >= 200 && status < 300:
			s.ring.Ack(n)
			s.sent.Add(uint64(n)) //nolint:gosec // a count of spans
			s.backoff = 0
			return n, 0
		case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
			return 0, otlp.Stretch(retryAfter, s.rnd)
		case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge:
			return s.refuse(n), 0
		default:
			return 0, s.fail()
		}
	}
}

// refuse drops the n oldest spans the server will not take
func (s *shipper) refuse(n int) int {
	s.ring.Ack(n)
	s.rejected.Add(uint64(n)) //nolint:gosec // a count of spans
	return n
}

// fail doubles the backoff, from 1 s to 60 s, and waits it jittered
func (s *shipper) fail() time.Duration {
	s.backoff = min(max(2*s.backoff, firstBackoff), maxBackoff)
	return otlp.Jitter(s.backoff, s.rnd)
}
