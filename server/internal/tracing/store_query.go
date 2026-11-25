package tracing

import (
	"slices"
	"sort"
	"sync"
	"time"
)

// list page sizes
const (
	DefaultListLimit = 50
	MaxListLimit     = 500
)

// snapshot copies the span for a reader, with its byte counters and, while it
// is live, the time it has run so far.
func (st *spanState) snapshot(now time.Time) SpanView {
	v := st.view
	if st.stream != nil {
		sv := SpanStreamView{BytesIn: st.stream.bytesIn, BytesOut: st.stream.bytesOut, SpeedIn: st.stream.speedIn, SpeedOut: st.stream.speedOut}
		if now.Sub(st.stream.lastAt) > speedStale {
			sv.SpeedIn, sv.SpeedOut = 0, 0
		}
		v.Stream = &sv
	}
	if v.Live && !v.Start.IsZero() {
		v.DurationMs = ms(now.Sub(v.Start))
	}
	return v
}

// ListQuery selects traces. Since is exclusive: a reader passes back the Seq
// of its last page to get the traces changed after it.
type ListQuery struct {
	Limit    int
	Live     bool
	DeviceID string
	Since    uint64
}

// TracePage is a list's answer: newest first by start. Seq is the Since of
// the reader's next query: the store's sequence, or the last change included
// when Limit cut a Since read short, so the next read loses nothing.
type TracePage struct {
	Traces []TraceSummary `json:"traces"`
	Seq    uint64         `json:"seq"`
}

// List answers q over the retained traces.
func (s *Store) List(q ListQuery) TracePage {
	if q.Limit <= 0 {
		q.Limit = DefaultListLimit
	}
	q.Limit = min(q.Limit, MaxListLimit)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	page := TracePage{Seq: s.seq, Traces: []TraceSummary{}}
	var picked []*traceState
	for _, t := range s.traces {
		switch {
		case len(t.spans) == 0, t.seq <= q.Since:
		case q.Live && t.live == 0:
		case q.DeviceID != "" && t.deviceID != q.DeviceID:
		default:
			picked = append(picked, t)
		}
	}
	if q.Since == 0 {
		sort.Slice(picked, func(i, j int) bool { return picked[i].start.After(picked[j].start) })
		picked = picked[:min(len(picked), q.Limit)]
	} else {
		// a resuming reader walks forward: the oldest changes first, cut at the limit
		sort.Slice(picked, func(i, j int) bool { return picked[i].seq < picked[j].seq })
		if len(picked) > q.Limit {
			picked = picked[:q.Limit]
			page.Seq = picked[len(picked)-1].seq
		}
		sort.Slice(picked, func(i, j int) bool { return picked[i].start.After(picked[j].start) })
	}
	for _, t := range picked {
		page.Traces = append(page.Traces, t.summary(now, nil))
	}
	return page
}

// Trace is one retained trace, false once the ring let it go.
func (s *Store) Trace(id string) (TraceView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.traces[id]
	if t == nil || len(t.spans) == 0 {
		return TraceView{}, false
	}
	now := s.now()
	out := TraceView{RootIDs: []string{}, Spans: make([]SpanView, 0, len(t.spans))}
	for _, id := range t.order {
		if st := t.spans[id]; st != nil {
			out.Spans = append(out.Spans, st.snapshot(now))
		}
	}
	sort.SliceStable(out.Spans, func(i, j int) bool { return out.Spans[i].Start.Before(out.Spans[j].Start) })
	for _, v := range out.Spans {
		if v.ParentID == "" || t.spans[v.ParentID] == nil {
			out.RootIDs = append(out.RootIDs, v.SpanID)
		}
	}
	out.TraceSummary = t.summary(now, out.Spans)
	return out, true
}

// summary is t as the list shows it; spans, when the caller has them, are
// its snapshots.
func (t *traceState) summary(now time.Time, spans []SpanView) TraceSummary {
	sum := TraceSummary{TraceID: t.id, Start: t.start, Live: t.live > 0, Error: t.hasError, Spans: len(t.spans),
		DeviceID: t.deviceID, Seq: t.seq, Services: []string{}}
	if spans == nil {
		for _, st := range t.spans {
			spans = append(spans, st.snapshot(now))
		}
	}
	var end time.Time
	var top *SpanView
	for i := range spans {
		v := &spans[i]
		e := v.Start.Add(time.Duration(v.DurationMs * float64(time.Millisecond)))
		if e.After(end) {
			end = e
		}
		if v.Service != "" && !slices.Contains(sum.Services, v.Service) {
			sum.Services = append(sum.Services, v.Service)
		}
		if (v.ParentID == "" || t.spans[v.ParentID] == nil) && (top == nil || v.Start.Before(top.Start)) {
			top = v
		}
	}
	slices.Sort(sum.Services)
	if top != nil {
		sum.Name = top.Name
	}
	if !end.IsZero() && !t.start.IsZero() {
		sum.DurationMs = ms(end.Sub(t.start))
	}
	return sum
}

// Since is every retained span changed after seq, oldest change first: an
// upsert list, so applying it in order converges on what the store holds.
func (s *Store) Since(seq uint64) []SpanView {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []SpanView
	for _, t := range s.traces {
		if t.seq <= seq {
			continue
		}
		for _, id := range t.order {
			if st := t.spans[id]; st != nil && st.view.Seq > seq {
				out = append(out, st.snapshot(now))
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// stats fills the store's half of Stats.
func (s *Store) stats() TraceStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return TraceStats{
		LiveSpans: len(s.live), Traces: len(s.traces), Finished: len(s.completed), RingSize: s.ringSize,
		Bytes: s.bytes, Budget: s.budget, Seq: s.seq, BytesIn: s.bytesIn, BytesOut: s.bytesOut,
		Dropped: s.dropped, Overflow: s.overflow, Foreign: s.foreign, Ingested: s.ingested, Rejected: s.rejected,
	}
}

// Subscribe returns a channel of the store's span events and its cancel. The
// channel is lossy: a reader that falls subBuf behind loses its oldest events
// (TraceStats.Dropped) rather than holding a span up, and recovers with Since.
func (s *Store) Subscribe() (<-chan SpanEvent, func()) {
	ch := make(chan SpanEvent, subBuf)
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	s.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subs, id)
			s.mu.Unlock()
			// publish sends only to channels still in the map, under the lock
			close(ch)
		})
	}
}

func (s *Store) publishAll(evs []SpanEvent) {
	for _, ev := range evs {
		s.publish(ev)
	}
}

// publish offers ev to every subscriber without blocking, dropping a full
// one's oldest event to make room.
func (s *Store) publish(ev SpanEvent) {
	if ev.Type == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- ev:
			continue
		default:
		}
		select {
		case <-ch:
			s.dropped++
		default:
		}
		select {
		case ch <- ev:
		default:
			s.dropped++
		}
	}
}
