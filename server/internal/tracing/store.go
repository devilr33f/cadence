package tracing

import (
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// store bounds and timings
const (
	defaultRingSize = 500
	minRingSize     = 10
	maxRingSize     = 5000
	// defaultBudget bounds the retained spans' estimated memory
	defaultBudget = 32 << 20
	// subBuf is how far a subscriber may fall behind before its oldest event goes
	subBuf = 1024
	// maxSpans bounds one trace: a loop must not grow one without limit
	maxSpans = 1024
	// maxLive bounds the spans begun and not ended, every trace together
	maxLive = 10000
	// leakAfter force-ends a span never ended; sweepEvery bounds the sweep
	leakAfter  = 24 * time.Hour
	sweepEvery = time.Minute
	// the byte counters: a speed over at least speedWindow, 0 after speedStale
	// of silence, smoothed by speedAlpha; updateEvery throttles span_update
	speedWindow = time.Second
	speedStale  = 2 * time.Second
	speedAlpha  = 0.5
	updateEvery = time.Second
	// spanOverhead is a span's estimated memory besides its strings
	spanOverhead = 400
)

// Store keeps the spans the processor and the device ingest feed it: every
// live span, and the last finished traces, bounded by count (the ring) and by
// estimated memory (the budget). It hands out copies, and never lets a slow
// reader block a span.
type Store struct {
	mu  sync.Mutex
	now func() time.Time

	ringSize int
	budget   int64
	bytes    int64

	// seq is stamped on every change, so a reader asks for what it has not
	// seen with one number
	seq       uint64
	traces    map[string]*traceState
	live      map[trace.SpanID]*spanState
	completed []string // finished trace ids, oldest first

	subs    map[int]chan SpanEvent
	nextSub int

	dropped, overflow, foreign uint64
	ingested, rejected         uint64
	bytesIn, bytesOut          int64
	lastSweep                  time.Time
}

type traceState struct {
	id       string
	spans    map[string]*spanState
	order    []string
	live     int
	hasError bool
	start    time.Time
	seq      uint64
	done     bool
	bytes    int64
	deviceID string
}

type spanState struct {
	id     trace.SpanID
	trace  string
	view   SpanView
	stream *streamState
	size   int64
}

// NewStore keeps ringSize finished traces (out of range: the default) within
// budget bytes (<= 0: the default).
func NewStore(ringSize int, budget int64) *Store {
	if budget <= 0 {
		budget = defaultBudget
	}
	return &Store{
		now: time.Now, ringSize: clampRing(ringSize), budget: budget,
		traces: map[string]*traceState{}, live: map[trace.SpanID]*spanState{}, subs: map[int]chan SpanEvent{},
	}
}

func clampRing(n int) int {
	if n <= 0 {
		return defaultRingSize
	}
	return min(max(n, minRingSize), maxRingSize)
}

// SetRingSize changes how many finished traces are kept, evicting the oldest
// at once when it shrinks.
func (s *Store) SetRingSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ringSize = clampRing(n)
	s.trimLocked()
}

func (s *Store) countForeign() {
	s.mu.Lock()
	s.foreign++
	s.mu.Unlock()
}

// start records a span that has just begun.
func (s *Store) start(v SpanView) {
	id, err := trace.SpanIDFromHex(v.SpanID)
	if err != nil {
		return
	}
	s.mu.Lock()
	now := s.now()
	swept := s.sweepLocked(now)
	t := s.traceLocked(v.TraceID, v.Start)
	if len(t.spans) >= maxSpans || len(s.live) >= maxLive || t.spans[v.SpanID] != nil {
		s.overflow++
		s.dropEmptyLocked(t)
		s.mu.Unlock()
		s.publishAll(swept)
		return
	}
	// a span starting under a finished trace puts it back in play, so the
	// ring never evicts something running
	if t.done {
		t.done = false
		s.removeCompletedLocked(t.id)
	}
	st := &spanState{id: id, trace: v.TraceID, view: v}
	st.view.Live, st.view.End = true, nil
	t.spans[v.SpanID] = st
	t.order = append(t.order, v.SpanID)
	t.live++
	s.live[id] = st
	s.sizeLocked(t, st)
	ev := s.touchLocked(t, st, EventSpanStart, now)
	s.mu.Unlock()
	s.publishAll(swept)
	s.publish(ev)
}

// end records a finished span, replacing what its start recorded: the final
// attributes, status and events come only now. A span with no start here
// (a device's, or one whose start overflowed) is recorded whole.
func (s *Store) end(v SpanView) {
	s.mu.Lock()
	now := s.now()
	swept := s.sweepLocked(now)
	ev, ok := s.endLocked(v, now)
	if ok {
		s.trimLocked()
	}
	s.mu.Unlock()
	s.publishAll(swept)
	if ok {
		s.publish(ev)
	}
}

// add records finished spans from elsewhere (a device), as end does each,
// and says how many it stored.
func (s *Store) add(views []SpanView) int {
	s.mu.Lock()
	now := s.now()
	swept := s.sweepLocked(now)
	evs := make([]SpanEvent, 0, len(views))
	for _, v := range views {
		if ev, ok := s.endLocked(v, now); ok {
			s.ingested++
			evs = append(evs, ev)
		} else {
			s.rejected++
		}
	}
	s.trimLocked()
	s.mu.Unlock()
	s.publishAll(swept)
	s.publishAll(evs)
	return len(evs)
}

func (s *Store) endLocked(v SpanView, now time.Time) (SpanEvent, bool) {
	id, err := trace.SpanIDFromHex(v.SpanID)
	if err != nil {
		return SpanEvent{}, false
	}
	t := s.traceLocked(v.TraceID, v.Start)
	st := t.spans[v.SpanID]
	if st == nil {
		if len(t.spans) >= maxSpans {
			s.overflow++
			s.dropEmptyLocked(t)
			return SpanEvent{}, false
		}
		st = &spanState{id: id, trace: v.TraceID}
		t.spans[v.SpanID] = st
		t.order = append(t.order, v.SpanID)
	} else if st.view.Live {
		t.live--
		delete(s.live, id)
	}
	stream := st.stream
	st.view = v
	st.view.Live = false
	st.stream = stream
	if v.Status == StatusError {
		t.hasError = true
	}
	s.sizeLocked(t, st)
	ev := s.touchLocked(t, st, EventSpanEnd, now)
	// a trace finishes when nothing in it runs: a device's spans arrive
	// finished, so a trace of them alone finishes as it arrives, and the
	// server's spans of a device's turn finish with the outermost one
	if t.live == 0 {
		s.finishLocked(t)
	}
	return ev, true
}

// traceLocked is id's trace, made if new.
func (s *Store) traceLocked(id string, start time.Time) *traceState {
	if t := s.traces[id]; t != nil {
		if !start.IsZero() && (t.start.IsZero() || start.Before(t.start)) {
			t.start = start
		}
		return t
	}
	t := &traceState{id: id, spans: map[string]*spanState{}, start: start}
	s.traces[id] = t
	return t
}

// dropEmptyLocked forgets a trace traceLocked made for a span then refused.
func (s *Store) dropEmptyLocked(t *traceState) {
	if len(t.spans) == 0 {
		delete(s.traces, t.id)
	}
}

// sizeLocked re-estimates st's memory into its trace's and the store's.
func (s *Store) sizeLocked(t *traceState, st *spanState) {
	n := estimate(&st.view)
	t.bytes += n - st.size
	s.bytes += n - st.size
	st.size = n
	if t.deviceID == "" {
		t.deviceID = spanDevice(&st.view)
	}
}

// spanDevice is the device a span names: its resource's, else its
// device.id attribute.
func spanDevice(v *SpanView) string {
	if v.DeviceID != "" {
		return v.DeviceID
	}
	if d, ok := v.Attrs[AttrDeviceID].(string); ok {
		return d
	}
	return ""
}

// estimate is a span's memory, roughly: its strings and a fixed overhead.
func estimate(v *SpanView) int64 {
	n := spanOverhead + len(v.Name) + len(v.Scope) + len(v.DeviceName) + attrsSize(v.Attrs)
	for _, e := range v.Events {
		n += 64 + len(e.Name) + attrsSize(e.Attrs)
	}
	n += 48 * len(v.Links)
	return int64(n)
}

func attrsSize(m map[string]any) int {
	n := 0
	for k, v := range m {
		n += len(k) + 16
		switch x := v.(type) {
		case string:
			n += len(x)
		case []string:
			for _, e := range x {
				n += len(e) + 16
			}
		}
	}
	return n
}

// touchLocked stamps a new sequence number on st and returns the event to
// publish once the lock is released.
func (s *Store) touchLocked(t *traceState, st *spanState, kind string, now time.Time) SpanEvent {
	s.seq++
	st.view.Seq = s.seq
	t.seq = s.seq
	return SpanEvent{Type: kind, Seq: s.seq, Span: st.snapshot(now)}
}
