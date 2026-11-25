package tracing

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	// spans audiod's marks may name at once: a turn has a capture and a few
	// streams, a screen a few calls; past it the oldest is ended as evicted
	maxBound = 64
	// a span bound longer than this is ended as expired at the next Bind: a
	// speaker that never says its stream is done must not keep a turn open
	maxBoundAge = 10 * time.Minute
	// a wake mark waits this long for the turn it opens
	wakeWait = 5 * time.Second
	// how far a mark's own time may stray from cadenced's clock before it is
	// taken as now (an unset or jumped clock)
	markSkew = time.Minute
)

// a value's key as audiod sends it: a fixed code, never text
var valueKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

type bound struct {
	span    trace.Span
	release func()
	seq     uint64
	at      time.Time
}

var marks = struct {
	sync.Mutex
	byID map[string]*bound
	seq  uint64
	// the last wake mark, until a turn takes it or it goes stale
	wake    *audiodv1.TraceMark
	wakeAt  time.Time
	dropped uint64
}{byID: map[string]*bound{}}

// now is the clock marks are checked against; a test's own
var now = time.Now

// Bind makes span the one audiod's marks naming id belong to (a capture's
// session id, a stream's id, an rpc's), until Release. release, if not nil,
// runs once the span ends (a playback lets its turn end).
func Bind(id string, span trace.Span, release func()) {
	if id == "" {
		return
	}
	marks.Lock()
	t := now()
	var gone []*bound
	for k, b := range marks.byID {
		if t.Sub(b.at) > maxBoundAge {
			gone = append(gone, b)
			delete(marks.byID, k)
		}
	}
	old := marks.byID[id]
	var evicted *bound
	if old == nil && len(marks.byID) >= maxBound {
		var oldestID string
		for k, b := range marks.byID {
			if evicted == nil || b.seq < evicted.seq {
				evicted, oldestID = b, k
			}
		}
		delete(marks.byID, oldestID)
	}
	marks.seq++
	marks.byID[id] = &bound{span: span, release: release, seq: marks.seq, at: t}
	marks.Unlock()
	for _, b := range gone {
		b.end("expired")
	}
	for _, b := range []*bound{old, evicted} {
		if b != nil {
			b.end("evicted")
		}
	}
}

// Release ends the span bound to id with code and attrs, and forgets it;
// false when none is (released already).
func Release(id, code string, attrs ...attribute.KeyValue) bool {
	marks.Lock()
	b := marks.byID[id]
	delete(marks.byID, id)
	marks.Unlock()
	if b == nil {
		return false
	}
	b.span.SetAttributes(attrs...)
	b.end(code)
	return true
}

// ReleasePrefix ends every span bound to an id starting with prefix (a
// screen's calls once it went).
func ReleasePrefix(prefix, code string) {
	marks.Lock()
	var gone []*bound
	for id, b := range marks.byID {
		if strings.HasPrefix(id, prefix) {
			gone = append(gone, b)
			delete(marks.byID, id)
		}
	}
	marks.Unlock()
	for _, b := range gone {
		b.end(code)
	}
}

// Bound is the span bound to id, nil for none.
func Bound(id string) trace.Span {
	marks.Lock()
	defer marks.Unlock()
	if b := marks.byID[id]; b != nil {
		return b.span
	}
	return nil
}

func (b *bound) end(code string) {
	End(b.span, code)
	if b.release != nil {
		b.release()
	}
}

// Mark records one of audiod's marks on the span its ids name: an event at
// the mark's time, or a child span when it lasted. A wake mark waits for the
// turn it opens (TakeWake); a mark whose ids name no span is dropped. It never
// blocks on anything slow.
func Mark(m *audiodv1.TraceMark) {
	if m.GetStage() == audiodv1.TraceStage_TRACE_STAGE_WAKE_DETECTED && m.GetSessionId() == "" && m.GetStreamId() == "" {
		marks.Lock()
		marks.wake, marks.wakeAt = m, now()
		marks.Unlock()
		return
	}
	var span trace.Span
	marks.Lock()
	for _, id := range []string{m.GetSessionId(), m.GetStreamId()} {
		if b := marks.byID[id]; id != "" && b != nil {
			span = b.span
			break
		}
	}
	if span == nil {
		marks.dropped++
	}
	marks.Unlock()
	if span != nil {
		record(span, m)
	}
}

// TakeWake records the last wake mark on span, a turn that just opened, if it
// came within the last 5 s.
func TakeWake(span trace.Span) {
	marks.Lock()
	m, at := marks.wake, marks.wakeAt
	marks.wake = nil
	marks.Unlock()
	if m != nil && now().Sub(at) <= wakeWait {
		record(span, m)
	}
}

// DroppedMarks counts the marks that named no traced span.
func DroppedMarks() uint64 {
	marks.Lock()
	defer marks.Unlock()
	return marks.dropped
}

func record(span trace.Span, m *audiodv1.TraceMark) {
	if !span.IsRecording() {
		return
	}
	at := markTime(m.GetAtUnixNs())
	name := StageName(m.GetStage())
	attrs := markAttrs(m.GetValues())
	if d := m.GetDurationNs(); d > 0 {
		_, child := tracer().Start(trace.ContextWithSpan(context.Background(), span), name, trace.WithTimestamp(at),
			trace.WithAttributes(attrs...))
		child.End(trace.WithTimestamp(at.Add(time.Duration(d))))
		return
	}
	span.AddEvent(name, trace.WithTimestamp(at), trace.WithAttributes(attrs...))
}

// markTime is a mark's instant, or now when it is unset or strays past a
// minute from cadenced's clock
func markTime(ns int64) time.Time {
	n := now()
	if ns <= 0 {
		return n
	}
	at := time.Unix(0, ns)
	if d := at.Sub(n); d > markSkew || d < -markSkew {
		return n
	}
	return at
}

func markAttrs(vs []*audiodv1.TraceValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(vs))
	for _, v := range vs {
		if valueKey.MatchString(v.GetKey()) {
			out = append(out, attribute.Float64("audiod."+v.GetKey(), v.GetValue()))
		}
	}
	return out
}

// StageName is a stage's event name: audiod.<stage>, or audiod.stage_<n> for
// one this build does not know.
func StageName(s audiodv1.TraceStage) string {
	name, ok := audiodv1.TraceStage_name[int32(s)]
	if !ok || s == audiodv1.TraceStage_TRACE_STAGE_UNSPECIFIED {
		return "audiod.stage_" + strconv.Itoa(int(s))
	}
	return "audiod." + strings.ToLower(strings.TrimPrefix(name, "TRACE_STAGE_"))
}
