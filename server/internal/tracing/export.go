package tracing

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	coltracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/trace/v1"
	tracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/trace/v1"
)

// export bounds
const (
	// spans waiting; past it the oldest go
	exportQueueSpans = 8192
	// spans in one request at most
	exportBatchSpans = 512
	exportBackoffMin = time.Second
	exportBackoffMax = time.Minute
)

// SignalTraces is the otlp signal traces are posted as (/v1/traces).
const SignalTraces = "traces"

// ExportClient posts one OTLP protobuf request of a signal; *otlphttp.Client
// is one, so traces reuse the telemetryExport instance's URL, auth and TLS.
// An error with Permanent() true refused the request itself; one with Wait()
// names how long the collector asked to be left alone.
type ExportClient interface {
	Post(ctx context.Context, signal string, request []byte) error
}

// Exporter forwards spans to the collector set as its target, best effort: a
// bounded queue it drops the oldest of when full, never blocking a span, and a
// backoff of 1 s doubling to 60 s after a failure.
type Exporter struct {
	log *slog.Logger

	mu     sync.Mutex
	queue  []*tracev1.ResourceSpans
	queued int
	target ExportClient
	wake   chan struct{}

	enabled                 atomic.Bool
	sent, dropped, failures atomic.Uint64
	backoffMin, backoffMax  time.Duration
}

func NewExporter(log *slog.Logger) *Exporter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	e := &Exporter{log: log, wake: make(chan struct{}, 1), backoffMin: exportBackoffMin, backoffMax: exportBackoffMax}
	e.enabled.Store(true)
	return e
}

// SetTarget makes c the collector; nil turns the export off and drops what
// waits.
func (e *Exporter) SetTarget(c ExportClient) {
	e.mu.Lock()
	e.target = c
	gone := 0
	if c == nil {
		gone = e.queued
		e.queue, e.queued = nil, 0
	}
	e.mu.Unlock()
	e.dropped.Add(uint64(gone)) //nolint:gosec // a count, never negative
	e.poke()
}

func (e *Exporter) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Enqueue queues one resource's spans, already guarded; nothing while no
// target is set or tracing is off.
func (e *Exporter) Enqueue(rs *tracev1.ResourceSpans) {
	n := spanCount(rs)
	if n == 0 || !e.enabled.Load() {
		return
	}
	e.mu.Lock()
	if e.target == nil {
		e.mu.Unlock()
		return
	}
	e.queue = append(e.queue, rs)
	e.queued += n
	gone := 0
	for e.queued > exportQueueSpans && len(e.queue) > 1 {
		k := spanCount(e.queue[0])
		e.queue = e.queue[1:]
		e.queued -= k
		gone += k
	}
	e.mu.Unlock()
	e.dropped.Add(uint64(gone)) //nolint:gosec // a count, never negative
	e.poke()
}

func spanCount(rs *tracev1.ResourceSpans) int {
	n := 0
	for _, ss := range rs.GetScopeSpans() {
		n += len(ss.GetSpans())
	}
	return n
}

// Stats are the export's counts since the server started.
func (e *Exporter) Stats() TraceExportStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return TraceExportStats{On: e.target != nil, Sent: e.sent.Load(), Dropped: e.dropped.Load(), Failures: e.failures.Load(), Queued: e.queued}
}

// Run sends the queue in order until stop; what waits then is left.
func (e *Exporter) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.loop(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// next takes up to exportBatchSpans spans' resources and the target, waiting
// for both.
func (e *Exporter) next(ctx context.Context) ([]*tracev1.ResourceSpans, ExportClient, bool) {
	for {
		e.mu.Lock()
		if e.target != nil && len(e.queue) > 0 {
			n, k := 0, 0
			for k < len(e.queue) && (k == 0 || n+spanCount(e.queue[k]) <= exportBatchSpans) {
				n += spanCount(e.queue[k])
				k++
			}
			batch := append([]*tracev1.ResourceSpans(nil), e.queue[:k]...)
			e.queue = e.queue[k:]
			e.queued -= n
			t := e.target
			e.mu.Unlock()
			return batch, t, true
		}
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, false
		case <-e.wake:
		}
	}
}

// putBack returns a failed batch to the head of the queue, unless the export
// was turned off or newer spans filled it meanwhile: then it is dropped.
func (e *Exporter) putBack(batch []*tracev1.ResourceSpans, n int) {
	e.mu.Lock()
	keep := e.target != nil && e.queued+n <= exportQueueSpans
	if keep {
		e.queue = append(batch, e.queue...)
		e.queued += n
	}
	e.mu.Unlock()
	if !keep {
		e.dropped.Add(uint64(n)) //nolint:gosec // a count, never negative
	}
}

type permanentError interface{ Permanent() bool }

type waitError interface{ Wait() time.Duration }

type codedError interface {
	Code() string
	Status() int
}

func (e *Exporter) loop(ctx context.Context) {
	backoff := e.backoffMin
	failing := 0
	for {
		batch, target, ok := e.next(ctx)
		if !ok {
			return
		}
		n := 0
		for _, rs := range batch {
			n += spanCount(rs)
		}
		req, err := proto.Marshal(&coltracev1.ExportTraceServiceRequest{ResourceSpans: batch})
		if err != nil {
			e.dropped.Add(uint64(n)) //nolint:gosec // a count, never negative
			continue
		}
		err = target.Post(ctx, SignalTraces, req)
		if err == nil {
			e.sent.Add(uint64(n)) //nolint:gosec // a count, never negative
			if failing > 0 {
				e.log.Info("trace_export_recovered", "failures", failing)
			}
			backoff, failing = e.backoffMin, 0
			continue
		}
		if ctx.Err() != nil {
			e.putBack(batch, n)
			return
		}
		e.failures.Add(1)
		if failing == 0 {
			e.logFailure(err)
		}
		failing++
		var perm permanentError
		if errors.As(err, &perm) && perm.Permanent() {
			e.dropped.Add(uint64(n)) //nolint:gosec // a count, never negative
		} else {
			e.putBack(batch, n)
		}
		wait := backoff
		var w waitError
		if errors.As(err, &w) {
			wait = max(wait, w.Wait())
		}
		backoff = min(backoff*2, e.backoffMax)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// logFailure names an outage's first failure by its code; never the
// collector's URL, header or answer.
func (e *Exporter) logFailure(err error) {
	code, status := "unavailable", 0
	var c codedError
	if errors.As(err, &c) {
		code, status = c.Code(), c.Status()
	}
	e.log.Warn("trace_export_failed", "code", code, "status", status)
}
