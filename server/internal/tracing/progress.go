package tracing

import (
	"context"
	"io"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// Progress reports bytes moved by the span in ctx, for a span that streams
// (a relayed song, a TTS stream): its counters cannot wait for its end.
// Callers report at most about once a second; the store emits an update that
// often at most anyway.
func Progress(ctx context.Context, in, out int64) {
	ProgressSpan(trace.SpanContextFromContext(ctx), in, out)
}

// ProgressSpan is Progress for a caller that kept the span context, not a
// context: a body wrapper outliving its request's context.
func ProgressSpan(sc trace.SpanContext, in, out int64) {
	if !enabled.Load() || (in == 0 && out == 0) || !sc.IsValid() {
		return
	}
	if s := current.Load(); s != nil {
		s.progress(sc.SpanID(), in, out)
	}
}

type streamState struct {
	bytesIn, bytesOut int64
	speedIn, speedOut float64
	lastAt            time.Time
	winAt             time.Time
	winIn, winOut     int64
	lastEmit          time.Time
}

// progress adds byte deltas to a live span and recomputes its speed.
func (s *Store) progress(id trace.SpanID, in, out int64) {
	s.mu.Lock()
	now := s.now()
	st := s.live[id]
	if st == nil {
		s.mu.Unlock()
		return
	}
	// the totals count spans that start their trace here: a relay's client
	// child moves the bytes its server root reports, and both would count twice
	if st.view.ParentID == "" || st.view.RemoteParent {
		s.bytesIn += in
		s.bytesOut += out
	}
	if st.stream == nil {
		st.stream = &streamState{}
	}
	st.stream.add(now, in, out)
	var ev SpanEvent
	if now.Sub(st.stream.lastEmit) >= updateEvery {
		st.stream.lastEmit = now
		if t := s.traces[st.trace]; t != nil {
			ev = s.touchLocked(t, st, EventSpanUpdate, now)
		}
	}
	s.mu.Unlock()
	s.publish(ev)
}

// add folds a delta into the counters, the speed recomputed once its window
// is a second at least: one large write over microseconds is no rate.
func (st *streamState) add(now time.Time, in, out int64) {
	st.bytesIn += in
	st.bytesOut += out
	st.lastAt = now
	if st.winAt.IsZero() {
		st.winAt, st.winIn, st.winOut = now, st.bytesIn-in, st.bytesOut-out
		return
	}
	elapsed := now.Sub(st.winAt)
	if elapsed < speedWindow {
		return
	}
	secs := elapsed.Seconds()
	st.speedIn = ewma(st.speedIn, float64(st.bytesIn-st.winIn)/secs)
	st.speedOut = ewma(st.speedOut, float64(st.bytesOut-st.winOut)/secs)
	st.winAt, st.winIn, st.winOut = now, st.bytesIn, st.bytesOut
}

// ewma takes the first sample as it is, so a speed shows after one window.
func ewma(prev, sample float64) float64 {
	if prev == 0 {
		return sample
	}
	return speedAlpha*sample + (1-speedAlpha)*prev
}

// CountReader reports the bytes read from r as bytes in on the span in ctx,
// about once a second and at EOF or an error. With nothing to report to it is
// r itself.
func CountReader(ctx context.Context, r io.Reader) io.Reader {
	sc := trace.SpanContextFromContext(ctx)
	if !enabled.Load() || r == nil || !sc.IsValid() {
		return r
	}
	return &countReader{r: r, c: counter{sc: sc, last: time.Now()}}
}

// CountWriter reports the bytes written to w as bytes out on the span in
// ctx, about once a second; Flush or Close on the counter is not needed: the
// last bytes are reported when a write fails or by FlushCount.
func CountWriter(ctx context.Context, w io.Writer) io.Writer {
	sc := trace.SpanContextFromContext(ctx)
	if !enabled.Load() || w == nil || !sc.IsValid() {
		return w
	}
	return &countWriter{w: w, c: counter{sc: sc, last: time.Now(), out: true}}
}

// FlushCount reports what a CountReader or CountWriter holds back; call it
// before ending the span. Anything else is left alone.
func FlushCount(x any) {
	switch c := x.(type) {
	case *countReader:
		c.c.flush(time.Now())
	case *countWriter:
		c.c.flush(time.Now())
	}
}

type counter struct {
	sc      trace.SpanContext
	pending int64
	last    time.Time
	out     bool
}

func (c *counter) count(n int, failed bool) {
	c.pending += int64(n)
	now := time.Now()
	if failed || now.Sub(c.last) >= updateEvery {
		c.flush(now)
	}
}

func (c *counter) flush(now time.Time) {
	c.last = now
	if c.pending == 0 {
		return
	}
	if c.out {
		ProgressSpan(c.sc, 0, c.pending)
	} else {
		ProgressSpan(c.sc, c.pending, 0)
	}
	c.pending = 0
}

type countReader struct {
	r io.Reader
	c counter
}

func (r *countReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.c.count(n, err != nil)
	return n, err
}

type countWriter struct {
	w io.Writer
	c counter
}

func (w *countWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.c.count(n, err != nil)
	return n, err
}
