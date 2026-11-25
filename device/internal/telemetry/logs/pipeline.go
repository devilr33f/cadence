package logs

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
)

const (
	ringBytes = 4 << 20
	// a batch goes out every batchEvery, or as soon as batchWaiting bytes wait
	batchEvery   = 2 * time.Second
	batchWaiting = 512 << 10
	firstBackoff = time.Second
	maxBackoff   = 60 * time.Second
	// sources come from code and board profiles; this bounds a mistake
	maxSources = 64
)

type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Stats is what the logs capability reports: records dropped from the full
// ring and never sent (one of a batch in flight counts once the batch is
// settled), records the server refused, the ring's bytes, and the sources
// that emitted since start, sorted, and the records kept that their source
// cut. Dropped, Rejected and Cut never go down.
type Stats struct {
	Dropped, Rejected, RingBytes, Cut uint64
	Sources                           []string
}

// Pipeline takes records from any goroutine (Emit) and ships them (Run). Its
// own trouble goes to the local log only, never back into the pipeline.
type Pipeline struct {
	client *otlp.Client
	scrub  *Scrubber
	log    *slog.Logger
	clock  clock
	// draws the exporter's jitter, in [0, 1)
	rnd    func() float64
	ring   *Ring
	levels atomic.Pointer[levels]
	// signalled once a batch's worth waits
	wake     chan struct{}
	rejected atomic.Uint64
	cut      atomic.Uint64

	mu      sync.Mutex
	sources map[string]bool
}

// New ships through client, redacting what scrub knows (nil: nothing). log
// must be the local log alone: a pipeline that logged into itself would loop.
func New(client *otlp.Client, scrub *Scrubber, log *slog.Logger) *Pipeline {
	if scrub == nil {
		scrub = &Scrubber{}
	}
	p := &Pipeline{
		client: client, scrub: scrub, log: log, clock: realClock{}, rnd: rand.Float64, ring: NewRing(ringBytes),
		wake: make(chan struct{}, 1), sources: map[string]bool{},
	}
	p.levels.Store(newLevels(nil, Info))
	return p
}

// SetLevels sets the minimum level per source, def for every other.
func (p *Pipeline) SetLevels(bySource map[string]Severity, def Severity) {
	p.levels.Store(newLevels(bySource, def))
}

// Emit filters, scrubs and keeps r, from any goroutine. It never blocks on the network.
func (p *Pipeline) Emit(r Record) {
	p.see(r.Source)
	if !p.levels.Load().keeps(r) {
		return
	}
	r.Body = p.scrub.scrubBody(r.Body)
	if r.Truncated {
		r.Body = p.scrub.scrubTail(strings.TrimSuffix(r.Body, ellipsis)) + ellipsis
		p.cut.Add(1)
	}
	if r.Continued {
		r.Body = ellipsis + p.scrub.scrubHead(strings.TrimPrefix(r.Body, ellipsis))
	}
	b, err := r.encode(p.clock.Now())
	if err != nil {
		p.ring.drop()
		return
	}
	if p.ring.Push(b) >= batchWaiting {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

func (p *Pipeline) see(source string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.sources[source] && len(p.sources) < maxSources {
		p.sources[source] = true
	}
}

func (p *Pipeline) Stats() Stats {
	p.mu.Lock()
	sources := slices.Sorted(maps.Keys(p.sources))
	p.mu.Unlock()
	return Stats{
		Dropped: p.ring.Dropped(), Rejected: p.rejected.Load(), Cut: p.cut.Load(),
		RingBytes: uint64(p.ring.Bytes()), Sources: sources, //nolint:gosec // never negative
	}
}

// Run ships batches through the client until ctx is done.
func (p *Pipeline) Run(ctx context.Context) {
	e := exporter{p: p}
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.clock.After(batchEvery):
		case <-p.wake:
		}
		if !e.flush(ctx) {
			return
		}
	}
}

type exporter struct {
	p       *Pipeline
	backoff time.Duration
	// within an episode of refused batches, which is logged once
	refusing bool
}

// flush sends what waits, oldest first, until less than a batch's worth is
// left; it reports false when ctx ended.
func (e *exporter) flush(ctx context.Context) bool {
	for {
		recs := e.p.ring.Peek(ringBytes)
		if len(recs) == 0 {
			return true
		}
		sent, wait := e.send(ctx, recs)
		if wait > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-e.p.clock.After(wait):
			}
			continue
		}
		if sent == len(recs) && e.p.ring.Bytes() < batchWaiting {
			return true
		}
	}
}

// send posts the oldest records of recs, halving them until a request fits
// the compressed cap, and returns how many are done with, or how long to
// wait before trying again.
func (e *exporter) send(ctx context.Context, recs [][]byte) (int, time.Duration) {
	n := len(recs)
	for {
		status, retryAfter, err := e.p.client.PostEncoded(ctx, otlp.LogsPath, otlp.LogsRequest(recs[:n]))
		switch {
		case errors.Is(err, otlp.ErrTooLarge) && n > 1:
			n /= 2
			continue
		case errors.Is(err, otlp.ErrTooLarge):
			// a single record is far below the cap; refuse it rather than stall the ring
			return e.refuse(0, 1), 0
		case err != nil:
			return 0, e.fail()
		case status >= 200 && status < 300:
			e.p.ring.Ack(n)
			e.backoff, e.refusing = 0, false
			return n, 0
		case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
			return 0, otlp.Stretch(retryAfter, e.p.rnd)
		case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge:
			return e.refuse(status, n), 0
		default:
			return 0, e.fail()
		}
	}
}

// refuse drops the n oldest records the server will not take.
func (e *exporter) refuse(status, n int) int {
	e.p.ring.Ack(n)
	e.p.rejected.Add(uint64(n)) //nolint:gosec // a count of records
	if !e.refusing {
		e.refusing = true
		e.p.log.Warn("logs_rejected", "status", status, "count", n)
	}
	return n
}

// fail doubles the backoff, from 1 s to 60 s, and waits it jittered: the next
// doubles from the backoff, not from the wait
func (e *exporter) fail() time.Duration {
	e.backoff = min(max(2*e.backoff, firstBackoff), maxBackoff)
	return otlp.Jitter(e.backoff, e.p.rnd)
}
