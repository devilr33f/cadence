package telemetry

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	selfQueue      = 4096
	selfBatch      = 500
	selfFlushEvery = time.Second
	// a batch's store call, and the last one's at stop
	selfStoreTimeout = 5 * time.Second
)

// LineSink takes the server's own lines, from any goroutine, never blocking.
type LineSink interface{ Add(r LogRow) }

// Tee is the server's log handler: each record goes to w as the JSON line it
// always did and, once a sink is attached, into it as a line of source server
// with no device and that same line as its body. The sink's own trouble is
// logged through Local, never through the tee, or it would loop back into it.
type Tee struct {
	h     slog.Handler
	sh    *teeShared
	local bool
}

// what a tee and every handler derived from it share
type teeShared struct {
	out  io.Writer
	sink atomic.Pointer[sinkBox]

	// held while a record is formatted and written, so lines keep their order
	mu  sync.Mutex
	buf []byte
}

type sinkBox struct{ s LineSink }

// the json handler formats into the shared buffer, under teeShared.mu
type capture struct{ sh *teeShared }

func (c capture) Write(p []byte) (int, error) {
	c.sh.buf = append(c.sh.buf, p...)
	return len(p), nil
}

// NewTee writes to w with slog's JSON handler and its default options.
func NewTee(w io.Writer) *Tee {
	sh := &teeShared{out: w}
	return &Tee{h: slog.NewJSONHandler(capture{sh}, nil), sh: sh}
}

// Attach makes every later record enter s too.
func (t *Tee) Attach(s LineSink) { t.sh.sink.Store(&sinkBox{s: s}) }

// Detach stops records entering the sink, as before its store closes.
func (t *Tee) Detach() { t.sh.sink.Store(nil) }

// Local logs to the tee's writer alone, in line with the tee's records.
func (t *Tee) Local() *slog.Logger { return slog.New(&Tee{h: t.h, sh: t.sh, local: true}) }

func (t *Tee) Enabled(ctx context.Context, l slog.Level) bool { return t.h.Enabled(ctx, l) }

func (t *Tee) Handle(ctx context.Context, r slog.Record) error {
	sh := t.sh
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.buf = sh.buf[:0]
	if err := t.h.Handle(ctx, r); err != nil {
		return err
	}
	if box := sh.sink.Load(); box != nil && !t.local {
		at := r.Time
		if at.IsZero() {
			at = time.Now()
		}
		body, cut := cutBody(string(trimNewline(sh.buf)))
		box.s.Add(LogRow{Time: at, Observed: at, Source: serverSource, Severity: severity(r.Level), Body: body, Truncated: cut})
	}
	_, err := sh.out.Write(sh.buf)
	return err
}

func (t *Tee) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Tee{h: t.h.WithAttrs(attrs), sh: t.sh, local: t.local}
}

func (t *Tee) WithGroup(name string) slog.Handler {
	return &Tee{h: t.h.WithGroup(name), sh: t.sh, local: t.local}
}

func trimNewline(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		return b[:n-1]
	}
	return b
}

// severity is the OTLP number of the level a slog level falls in, a level
// between two taken as the lower
func severity(l slog.Level) int16 {
	switch {
	case l >= slog.LevelError:
		return 17
	case l >= slog.LevelWarn:
		return 13
	case l >= slog.LevelInfo:
		return 9
	}
	return 5
}

// LineStore keeps lines; *LogStore is one.
type LineStore interface {
	Insert(ctx context.Context, rows []LogRow) error
}

// SelfLog is the sink of the server's own lines: a bounded queue a writer
// stores in batches of at most 500, or what a second gathered. A line the
// queue has no room for, and a batch the store refused, are dropped and
// counted.
type SelfLog struct {
	store   LineStore
	local   *slog.Logger
	queue   chan LogRow
	dropped atomic.Uint64
	every   time.Duration
	timeout time.Duration
}

func NewSelfLog(store LineStore, local *slog.Logger) *SelfLog {
	return &SelfLog{store: store, local: local, queue: make(chan LogRow, selfQueue), every: selfFlushEvery,
		timeout: selfStoreTimeout}
}

func (s *SelfLog) Add(r LogRow) {
	select {
	case s.queue <- r:
	default:
		s.dropped.Add(1)
	}
}

// Dropped counts the lines never stored.
func (s *SelfLog) Dropped() uint64 { return s.dropped.Load() }

// Run starts the writer; stop stores what is queued, then ends it. A stop
// takes one store timeout at most, the database gone or not: every store
// call, the one under way included, ends by then, and what is left of the
// queue after a failed one is dropped and counted.
func (s *SelfLog) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	stores, cut := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		w := &selfWriter{s: s, ctx: stores}
		timer := time.NewTimer(s.every)
		timer.Stop()
		for {
			// a stop is seen before more of the queue
			if ctx.Err() != nil {
				w.drain()
				return
			}
			select {
			case r := <-s.queue:
				if w.add(r) == 1 {
					timer.Reset(s.every)
				}
				if len(w.batch) >= selfBatch {
					timer.Stop()
					w.flush()
				}
			case <-timer.C:
				w.flush()
			case <-ctx.Done():
				timer.Stop()
			}
		}
	}()
	return func() {
		cancel()
		deadline := time.AfterFunc(s.timeout, cut)
		<-done
		deadline.Stop()
		cut()
	}
}

// selfWriter is the writer goroutine's state
type selfWriter struct {
	s *SelfLog
	// the store calls' context, cut a store timeout after a stop
	ctx     context.Context
	batch   []LogRow
	failing bool
}

func (w *selfWriter) add(r LogRow) int {
	w.batch = append(w.batch, r)
	return len(w.batch)
}

// drain stores what is queued as the server stops, never more than that: a
// line still arriving cannot hold the stop up. After a failed batch the rest
// is dropped and counted: the store would fail it too, or run out the stop's time.
func (w *selfWriter) drain() {
	// the writer is the queue's one reader, so its n lines are there to take
	for n := len(w.s.queue); n > 0; n-- {
		if w.add(<-w.s.queue) >= selfBatch && !w.flush() {
			w.s.dropped.Add(uint64(n - 1)) //nolint:gosec // a length
			for range n - 1 {
				<-w.s.queue
			}
			return
		}
	}
	w.flush()
}

// flush stores the batch, reporting whether it was stored.
func (w *selfWriter) flush() bool {
	if len(w.batch) == 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(w.ctx, w.s.timeout)
	err := w.s.store.Insert(ctx, w.batch)
	cancel()
	n := len(w.batch)
	w.batch = w.batch[:0:0]
	if err == nil {
		w.failing = false
		return true
	}
	w.s.dropped.Add(uint64(n)) //nolint:gosec // a length
	if !w.failing {
		// once an outage, and only here: through the tee it would queue itself
		w.failing = true
		w.s.local.Warn("server_logs_failed", "dropped", n)
	}
	return false
}
