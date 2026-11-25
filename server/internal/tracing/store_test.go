package tracing

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func tid(n int) string { return fmt.Sprintf("%032x", n+1) }
func sid(n int) string { return fmt.Sprintf("%016x", n+1) }

func testStore(ring int, budget int64) (*Store, *time.Time) {
	s := NewStore(ring, budget)
	now := t0
	s.now = func() time.Time { return now }
	return s, &now
}

func span(trace, id, parent string, start time.Time, d time.Duration) SpanView {
	end := start.Add(d)
	return SpanView{TraceID: trace, SpanID: id, ParentID: parent, Name: "op", Service: serviceServer, Start: start, End: &end, DurationMs: ms(d)}
}

func live(trace, id, parent string, start time.Time) SpanView {
	v := span(trace, id, parent, start, 0)
	v.End, v.Live = nil, true
	return v
}

func TestRingKeepsTheNewestFinished(t *testing.T) {
	s, _ := testStore(10, 0)
	for i := range 15 {
		s.end(span(tid(i), sid(i), "", t0.Add(time.Duration(i)*time.Second), time.Millisecond))
	}
	st := s.stats()
	if st.Traces != 10 || st.Finished != 10 {
		t.Fatalf("stats %+v", st)
	}
	if _, ok := s.Trace(tid(4)); ok {
		t.Fatal("oldest kept")
	}
	if _, ok := s.Trace(tid(5)); !ok {
		t.Fatal("tenth newest evicted")
	}
	s.SetRingSize(10)
	s.SetRingSize(1) // clamped to the minimum
	if s.stats().RingSize != minRingSize {
		t.Fatalf("ring %d", s.stats().RingSize)
	}
}

func TestBudgetEvictsButNeverALiveTrace(t *testing.T) {
	s, _ := testStore(100, 20_000)
	s.start(live(tid(0), sid(0), "", t0))
	big := strings.Repeat("x", 200)
	for i := 1; i < 60; i++ {
		v := span(tid(i), sid(i), "", t0.Add(time.Duration(i)*time.Second), time.Millisecond)
		v.Attrs = map[string]any{"a": big, "b": big, "c": big}
		s.end(v)
	}
	st := s.stats()
	if st.Bytes > st.Budget {
		t.Fatalf("over budget with finished traces left: %+v", st)
	}
	if st.Finished >= 59 {
		t.Fatalf("nothing evicted: %+v", st)
	}
	if tr, ok := s.Trace(tid(0)); !ok || !tr.Live {
		t.Fatal("the live trace was evicted")
	}
}

func TestLiveSpanAndItsEnd(t *testing.T) {
	s, now := testStore(10, 0)
	ch, cancel := s.Subscribe()
	defer cancel()
	s.start(live(tid(0), sid(0), "", t0))
	*now = t0.Add(2 * time.Second)
	page := s.List(ListQuery{Live: true})
	if len(page.Traces) != 1 || !page.Traces[0].Live || page.Traces[0].DurationMs != 2000 {
		t.Fatalf("live list %+v", page)
	}
	s.end(span(tid(0), sid(0), "", t0, 3*time.Second))
	if page := s.List(ListQuery{Live: true}); len(page.Traces) != 0 {
		t.Fatalf("finished trace still live %+v", page)
	}
	first, second := <-ch, <-ch
	if first.Type != EventSpanStart || !first.Span.Live || second.Type != EventSpanEnd || second.Span.Live ||
		second.Span.DurationMs != 3000 || second.Seq <= first.Seq {
		t.Fatalf("events %+v %+v", first, second)
	}
}

// a server span whose remote parent is the device's voice.turn shows as a
// root until the device's spans arrive, then as its child: one trace, one root
func TestDeviceSpansMergeIntoTheServerTrace(t *testing.T) {
	s, _ := testStore(10, 0)
	trace := tid(7)
	turn, wake, server, llm := sid(1), sid(2), sid(3), sid(4)
	srv := live(trace, server, turn, t0.Add(300*time.Millisecond))
	srv.RemoteParent = true
	s.start(srv)
	s.start(live(trace, llm, server, t0.Add(400*time.Millisecond)))
	tr, _ := s.Trace(trace)
	if !tr.Live || len(tr.RootIDs) != 1 || tr.RootIDs[0] != server {
		t.Fatalf("before the device: %+v", tr)
	}
	s.end(span(trace, llm, server, t0.Add(400*time.Millisecond), time.Second))
	srvEnd := span(trace, server, turn, t0.Add(300*time.Millisecond), 1500*time.Millisecond)
	srvEnd.RemoteParent = true
	s.end(srvEnd)
	if tr, _ := s.Trace(trace); tr.Live || s.stats().Finished != 1 {
		t.Fatalf("server spans ended, trace still live: %+v", tr)
	}
	// the device's batch, after the turn ended there: its root and a stage
	dev := func(v SpanView) SpanView {
		v.Service, v.DeviceID, v.DeviceName, v.Board = serviceDevice, "dev-1", "kitchen", "sim"
		return v
	}
	if n := s.add([]SpanView{dev(span(trace, turn, "", t0, 2*time.Second)), dev(span(trace, wake, turn, t0, 200*time.Millisecond))}); n != 2 {
		t.Fatalf("stored %d", n)
	}
	tr, ok := s.Trace(trace)
	if !ok || len(tr.Spans) != 4 || len(tr.RootIDs) != 1 || tr.RootIDs[0] != turn {
		t.Fatalf("after the device: roots %v spans %d", tr.RootIDs, len(tr.Spans))
	}
	if tr.Name != "op" || tr.DeviceID != "dev-1" || tr.DurationMs != 2000 || tr.Live ||
		strings.Join(tr.Services, ",") != "cadence-device,cadence-server" {
		t.Fatalf("summary %+v", tr.TraceSummary)
	}
	if tr.Spans[0].SpanID != turn {
		t.Fatalf("spans not by start: %s first", tr.Spans[0].SpanID)
	}
	if st := s.stats(); st.Traces != 1 || st.Finished != 1 || st.Ingested != 2 {
		t.Fatalf("stats %+v", st)
	}
	if page := s.List(ListQuery{DeviceID: "dev-1"}); len(page.Traces) != 1 {
		t.Fatalf("device filter %+v", page)
	}
	if page := s.List(ListQuery{DeviceID: "dev-2"}); len(page.Traces) != 0 {
		t.Fatalf("other device %+v", page)
	}
}

func TestListSinceResumesWithoutAGap(t *testing.T) {
	s, _ := testStore(100, 0)
	for i := range 5 {
		s.end(span(tid(i), sid(i), "", t0.Add(time.Duration(i)*time.Second), time.Millisecond))
	}
	all := s.List(ListQuery{})
	if len(all.Traces) != 5 || all.Traces[0].TraceID != tid(4) || all.Seq != 5 {
		t.Fatalf("newest first %+v", all)
	}
	if got := s.List(ListQuery{Limit: 2}); len(got.Traces) != 2 || got.Traces[0].TraceID != tid(4) {
		t.Fatalf("limit %+v", got)
	}
	// a reader at seq 1 asks for two: the two oldest changes after it, and a
	// seq that stops at the last it got
	page := s.List(ListQuery{Since: 1, Limit: 2})
	if len(page.Traces) != 2 || page.Seq != 3 || page.Traces[0].TraceID != tid(2) || page.Traces[1].TraceID != tid(1) {
		t.Fatalf("since page %+v", page)
	}
	next := s.List(ListQuery{Since: page.Seq, Limit: 2})
	if len(next.Traces) != 2 || next.Traces[1].TraceID != tid(3) {
		t.Fatalf("next page %+v", next)
	}
	if spans := s.Since(3); len(spans) != 2 || spans[0].Seq != 4 {
		t.Fatalf("since spans %+v", spans)
	}
}

func TestSlowSubscriberLosesItsOldestAndRecovers(t *testing.T) {
	s, _ := testStore(5000, 0)
	ch, cancel := s.Subscribe()
	for i := range subBuf + 10 {
		s.end(span(tid(i), sid(i), "", t0, time.Millisecond))
	}
	if s.stats().Dropped != 10 {
		t.Fatalf("dropped %d", s.stats().Dropped)
	}
	if ev := <-ch; ev.Seq != 11 {
		t.Fatalf("oldest kept %d", ev.Seq)
	}
	// what it lost is still there to read
	if spans := s.Since(0); len(spans) != subBuf+10 || spans[0].Seq != 1 {
		t.Fatalf("since %d", len(spans))
	}
	cancel()
	cancel()
	s.end(span(tid(9999), sid(0), "", t0, time.Millisecond))
}

func TestOverflowAndLeak(t *testing.T) {
	s, now := testStore(10, 0)
	for i := range maxSpans + 1 {
		s.start(live(tid(0), sid(i), "", t0))
	}
	if st := s.stats(); st.Overflow != 1 || st.LiveSpans != maxSpans {
		t.Fatalf("overflow %+v", st)
	}
	// a span never ended is ended by the sweep, failed as leaked
	*now = t0.Add(leakAfter + 2*time.Minute)
	s.end(span(tid(1), sid(0), "", *now, time.Millisecond))
	tr, ok := s.Trace(tid(0))
	if !ok || tr.Live || !tr.Error || tr.Spans[0].StatusCode != "leaked" {
		t.Fatalf("leak %+v", tr.TraceSummary)
	}
}

func TestProgressCountsBytesAndThrottlesUpdates(t *testing.T) {
	s, now := testStore(10, 0)
	ch, cancel := s.Subscribe()
	defer cancel()
	s.start(live(tid(0), sid(0), "", t0))
	<-ch
	id := mustSpanID(t, sid(0))
	s.progress(id, 1000, 0)
	if first := <-ch; first.Span.Stream.BytesIn != 1000 {
		t.Fatalf("first update %+v", first.Span.Stream)
	}
	*now = t0.Add(500 * time.Millisecond)
	s.progress(id, 1000, 0)
	*now = t0.Add(1100 * time.Millisecond)
	s.progress(id, 1000, 500)
	ev := <-ch
	if ev.Type != EventSpanUpdate || ev.Span.Stream == nil || ev.Span.Stream.BytesIn != 3000 || ev.Span.Stream.BytesOut != 500 {
		t.Fatalf("update %+v", ev)
	}
	select {
	case extra := <-ch:
		t.Fatalf("more than one update a second: %+v", extra)
	default:
	}
	if ev.Span.Stream.SpeedIn < 2000 || ev.Span.Stream.SpeedIn > 3000 {
		t.Fatalf("speed %v", ev.Span.Stream.SpeedIn)
	}
	if st := s.stats(); st.BytesIn != 3000 || st.BytesOut != 500 {
		t.Fatalf("totals %+v", st)
	}
	*now = t0.Add(5 * time.Second)
	tr, _ := s.Trace(tid(0))
	if tr.Spans[0].Stream.SpeedIn != 0 {
		t.Fatal("a stalled stream keeps its speed")
	}
}
