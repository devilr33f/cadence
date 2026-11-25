// Package tracing is the server's distributed tracing: an OpenTelemetry SDK
// TracerProvider whose spans land in an in-memory store (live spans and a
// bounded ring of finished traces), are published on the live feed's traces
// topic, and are exported over OTLP/HTTP to the collector the telemetryExport
// setting names. Devices' spans arrive at the OTLP ingest and merge into the
// same traces. It is named tracing so that go.opentelemetry.io/otel/trace
// stays importable beside it.
//
// # Instrumenting (phase 2 and later)
//
// Start a span with Start (or StartKind, StartAt, Background) and close it
// with End:
//
//	ctx, span := tracing.Start(ctx, "agent", "agent.step", attribute.Int("cadence.step", n))
//	defer func() { tracing.End(span, err) }()
//
// The scope ("agent") becomes "cadence/agent"; spans of any other scope
// (libraries that speak the otel api) are counted and dropped. While tracing
// is off Start returns ctx and a non-recording span, so callers never check.
// StartAt backdates a span to work that began before the code saw it (the
// wake word audiod heard); stages known later go on as events with
// trace.WithTimestamp. Background starts a new trace for work that outlives
// its request, linked to the span that spawned it. Progress, CountReader and
// CountWriter count a streaming span's bytes and speeds.
//
// Outbound HTTP: Transport wraps an http.RoundTripper (httpx.Client uses it,
// so every provider client has it) to give each request made under a
// recording span a client span named "METHOD host", with its status and body
// sizes, and a traceparent header. A 101 ends it at the handshake, OK: the
// body is then the websocket itself, handed on unwrapped, and its life is
// the provider's own span (uniproxy.stt, for one). Inbound HTTP: Middleware gives each /api
// request a server span named "METHOD /route/:template" and joins a caller's
// traceparent.
//
// Across Cadence's own hops (the device link's frames, the JSON RPC) the
// context rides as pkg/tracectx's Carrier; see that package for which frames
// carry it. The link is one long-lived websocket and never one span: each
// frame that carries a Carrier is extracted on its own (tracectx.Extract),
// and the span started from it is that frame's work.
//
// # Privacy (rule 8): what a span may carry
//
// Span names, attributes and events carry ids, codes, numbers, sizes and
// timings only. Never a transcript, a prompt, an LLM message or tool
// argument, media metadata (titles, artists, covers), a URL with its path or
// query, a header, a token or a secret, nor an error's text: End records an
// error's class (below), never err.Error(). Name spans by what they do
// ("llm.request", "GET /api/v1/devices/:id", "POST api.deepgram.com"), never
// by data. A span's device is device.id (AttrDeviceID); a provider is
// cadence.provider by its instance name.
//
// # Errors: classified, never quoted
//
// End(span, err) and FailErr record err by Classify, from its types and
// numbers alone: its own code (a Code() or ErrorCode() that is a token),
// else canceled, timeout (a context's deadline, a net timeout, ETIMEDOUT),
// ws_close (a coder/websocket CloseError, its status as
// websocket.close_code), dns, conn_refused, conn_reset (ECONNRESET, EPIPE,
// ECONNABORTED), conn_closed (net.ErrClosed), tls (record header, alert,
// x509), eof, status_<n> (an HTTPStatusCode()), else error. The class is the
// status code and error.type; error.go_type is the chain's type names
// (at most 3, wrappers left out); and one "exception" event carries
// error.type, exception.type and the go types, never exception.message.
// Fail(span, code) stays for a code the caller already has. A stream that
// knows more says it as attributes of its own: cadence.outcome (how a
// socket ended: invalid_auth, closed…), cadence.detail (one token:
// closed_1011, asr_error_3, dial_status_401), websocket.close_reason only
// when it is a token (^[A-Za-z0-9_.:-]{1,48}$). A failed voice.session
// names cadence.failed_stage, failed_span, failed_detail and failed_step.
//
// The guard under it is a safety net, not a licence: on every span the store
// keeps and the export sends, local or a device's, it drops attribute keys
// that are or contain url.full, http.url, url.query, http.target, query,
// token, secret, password, authorization, cookie, api_key, credential,
// prompt, completion, transcript, message, content, title, artist, lyrics or
// header.; cuts strings to 256 bytes, names to 128 and slices to 32
// elements; and keeps a status description only when it is a code
// (^[a-z0-9][a-z0-9_.:-]{0,63}$), else "error". A device's string values are
// also scrubbed of its token and capability secrets, as its log bodies are.
//
// # Devices' spans (phase 3)
//
// cadenced posts OTLP/HTTP protobuf ExportTraceServiceRequests, gzipped or
// not, to POST /v2/otlp/v1/traces with its device token, as it posts logs
// and metrics (the same bucket, 4 MiB decompressed at most, one request at a
// time; 2048 spans a request). Its resource is replaced by the server's
// stamp: service.name cadence-device, device.id, device.name, board (and the
// device's service.version kept). Spans more than 24 h off the server's
// clock, with malformed ids or an end before their start are rejected and
// counted in the answer's partial success. Stored spans are forwarded to the
// export too.
//
// A device span and a server span of one trace merge: the tree is built by
// parentId alone, so a server span whose remote parent is the device's
// voice.turn shows as a root until the device's batch arrives, then as its
// child (TraceView.RootIDs is computed on each read). A trace is live while
// any span in it runs, and finished when none does; device spans arrive
// finished, and a trace that already finished takes them in place.
//
// # REST (admin scope), under /api/v1
//
//   - GET /traces?limit=50&live=&device=<uuid>&since=<seq> → TracePage
//     {traces: [TraceSummary], seq}: newest first by start. With since, the
//     traces changed after it, cut at limit oldest change first, and seq is
//     the last one included, so a reader resumes without a gap.
//   - GET /traces/{id} → TraceView: the summary, rootIds and every span
//     (SpanView) by start; 404 not_found once the ring let it go.
//   - GET /traces/stats → TraceStats.
//
// # Live feed: the traces topic (admin, opt-in)
//
// A feed subscribed to "traces" gets span_start, span_update (a streaming
// span's counters, at most one a second a span) and span_end frames:
//
//	{"type":"span_end","deviceId":"<uuid>"?,"at":"<span start>","data":{"seq":42,"span":{SpanView}}}
//
// Each carries the whole span (an upsert by spanId). A feed gets at most 200
// a second, the rest counted in a span_dropped {count} frame a second; a
// reader that missed some reads GET /traces?since=<last seq> and
// GET /traces/{id}.
package tracing
