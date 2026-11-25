package tracing

import "time"

// Span statuses as plain strings; an unset status is "".
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Live feed event types (the traces topic). A reader applies them as upserts
// keyed by spanId: every event carries the whole span, never a delta, so a
// dropped one costs freshness only and the next repairs the view.
const (
	// EventSpanStart is a span that has just begun.
	EventSpanStart = "span_start"
	// EventSpanUpdate is a live span's new byte counters, at most one a
	// second a span.
	EventSpanUpdate = "span_update"
	// EventSpanEnd is a span that finished, or arrived finished from a
	// device.
	EventSpanEnd = "span_end"
)

// SpanView is one span as every surface sees it: the REST answers, the live
// feed's events and the store's records. It is a value: the store hands out
// copies.
type SpanView struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	// ParentID is empty for a span that starts its trace. A tree is built by
	// it alone: a span whose parent is not in the trace is a top-level one.
	ParentID string `json:"parentId,omitempty"`
	// RemoteParent says the parent lives in another process (a device's
	// voice.turn, a caller's traceparent); it may arrive later from a device.
	RemoteParent bool   `json:"remoteParent,omitempty"`
	Name         string `json:"name"`
	// Scope is the instrumentation scope without its "cadence/" prefix
	// ("api", "agent", "http"…); a device's spans keep theirs.
	Scope string `json:"scope,omitempty"`
	// Kind is internal, server, client, producer or consumer.
	Kind string `json:"kind,omitempty"`
	// Service is the process that recorded the span: cadence-server or
	// cadence-device.
	Service string `json:"service"`
	// DeviceID, DeviceName and Board stamp a device's span (its resource), as
	// the telemetry export stamps its lines.
	DeviceID   string    `json:"deviceId,omitempty"`
	DeviceName string    `json:"deviceName,omitempty"`
	Board      string    `json:"board,omitempty"`
	Start      time.Time `json:"start"`
	// End is unset while the span is live.
	End *time.Time `json:"end,omitempty"`
	// DurationMs is final once the span ended, else measured to the read.
	DurationMs float64 `json:"durationMs"`
	Status     string  `json:"status,omitempty" enum:"ok,error"`
	// StatusCode is an error's code (a token such as timeout or status_503),
	// never an error's text.
	StatusCode string          `json:"statusCode,omitempty"`
	Attrs      map[string]any  `json:"attrs,omitempty"`
	Events     []SpanEventView `json:"events,omitempty"`
	Links      []SpanLinkView  `json:"links,omitempty"`
	Live       bool            `json:"live"`
	// Stream is set on a span that moves bytes (Progress, CountReader).
	Stream *SpanStreamView `json:"stream,omitempty"`
	// Seq is the store-wide sequence number of the span's last change.
	Seq uint64 `json:"seq"`
}

// SpanStreamView are a span's byte counters; the speeds are bytes a second over
// at least a second, smoothed, and 0 once nothing moved for two seconds.
type SpanStreamView struct {
	BytesIn  int64   `json:"bytesIn"`
	BytesOut int64   `json:"bytesOut"`
	SpeedIn  float64 `json:"speedIn"`
	SpeedOut float64 `json:"speedOut"`
}

// SpanEventView is a point in a span's life: a stage's timing, a retry.
type SpanEventView struct {
	Name  string         `json:"name"`
	At    time.Time      `json:"at"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// SpanLinkView points at a span of another trace: the request that started a
// background job.
type SpanLinkView struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
}

// SpanEvent is one change the store publishes.
type SpanEvent struct {
	Type string   `json:"type"`
	Seq  uint64   `json:"seq"`
	Span SpanView `json:"span"`
}

// TraceSummary is a trace as the list shows it.
type TraceSummary struct {
	TraceID string `json:"traceId"`
	// Name is the earliest top-level span's name.
	Name       string    `json:"name"`
	Start      time.Time `json:"start"`
	DurationMs float64   `json:"durationMs"`
	Live       bool      `json:"live"`
	Error      bool      `json:"error"`
	Spans      int       `json:"spans"`
	// DeviceID is the first device a span names (its resource, or its
	// device.id attribute).
	DeviceID string `json:"deviceId,omitempty"`
	// Services are the processes whose spans the trace holds.
	Services []string `json:"services"`
	Seq      uint64   `json:"seq"`
}

// TraceView is one whole trace, its spans ordered by start.
type TraceView struct {
	TraceSummary
	// RootIDs are the top-level spans: no parent, or a parent not recorded
	// (yet). One device span arriving can make a root a child.
	RootIDs []string   `json:"rootIds"`
	Spans   []SpanView `json:"spans"`
}

// TraceStats summarise the store and the export.
type TraceStats struct {
	Enabled     bool    `json:"enabled"`
	SampleRatio float64 `json:"sampleRatio"`
	// LiveSpans are spans begun and not ended.
	LiveSpans int `json:"liveSpans"`
	// Traces are retained ones, live and finished; Finished the ring's.
	Traces   int `json:"traces"`
	Finished int `json:"finished"`
	RingSize int `json:"ringSize"`
	// Bytes is the retained spans' estimated memory, Budget its cap.
	Bytes  int64  `json:"bytes"`
	Budget int64  `json:"budget"`
	Seq    uint64 `json:"seq"`
	// BytesIn and BytesOut are the byte counters' totals since start, of
	// spans that start their trace here.
	BytesIn  int64 `json:"bytesIn"`
	BytesOut int64 `json:"bytesOut"`
	// Dropped counts events a slow subscriber missed; Overflow spans
	// refused because their trace, or the live set, was full; Foreign spans
	// of instrumentation that is not Cadence's.
	Dropped  uint64 `json:"dropped"`
	Overflow uint64 `json:"overflow"`
	Foreign  uint64 `json:"foreign"`
	// Ingested counts device spans stored, Rejected device spans refused.
	Ingested uint64 `json:"ingested"`
	Rejected uint64 `json:"rejected"`
	// Export is the OTLP export's counts.
	Export TraceExportStats `json:"export"`
}

// TraceExportStats count spans sent to the collector and dropped on the way.
type TraceExportStats struct {
	On       bool   `json:"on"`
	Sent     uint64 `json:"sent"`
	Dropped  uint64 `json:"dropped"`
	Failures uint64 `json:"failures"`
	Queued   int    `json:"queued"`
}
