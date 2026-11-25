import type { SpanView, TraceView } from '@/api/gen'

// the spans whose parent is not in the trace, by start: the server's rule, read again as spans arrive
export function rootIdsOf(spans: readonly SpanView[]): string[] {
  const ids = new Set(spans.map(s => s.spanId))
  return spans
    .filter(s => !s.parentId || !ids.has(s.parentId))
    .sort((a, b) => Date.parse(a.start) - Date.parse(b.start))
    .map(s => s.spanId)
}

function endOf(span: SpanView): number {
  return Date.parse(span.start) + Math.max(0, span.durationMs)
}

// a trace's summary fields from its spans, as the server sums them
function summed(view: TraceView, spans: SpanView[]): TraceView {
  const rootIds = rootIdsOf(spans)
  const start = Math.min(...spans.map(s => Date.parse(s.start)))
  const end = Math.max(...spans.map(endOf))
  const top = spans.find(s => s.spanId === rootIds[0])
  return {
    ...view,
    spans,
    rootIds,
    name: top?.name ?? view.name,
    start: new Date(start).toISOString(),
    durationMs: Math.max(0, end - start),
    live: spans.some(s => s.live),
    error: spans.some(s => s.status === 'error'),
    services: [
      ...new Set(spans.map(s => s.service).filter(Boolean)),
    ].sort(),
    seq: Math.max(view.seq, ...spans.map(s => s.seq)),
  }
}

// a span from the feed folded into a shown trace: a newer copy replaces the old by spanId, a new one joins by start.
// the same view when it told nothing new
export function upsertSpan(view: TraceView, span: SpanView): TraceView {
  if (span.traceId !== view.traceId)
    return view
  const spans = view.spans ?? []
  const at = spans.findIndex(s => s.spanId === span.spanId)
  if (at >= 0 && spans[at]!.seq >= span.seq)
    return view
  const next = at >= 0
    ? spans.with(at, span)
    : [...spans, span]
  next.sort((a, b) => Date.parse(a.start) - Date.parse(b.start))
  return summed(view, next)
}

// a trace the feed told of before its first read: what the page shows until the read lands
export function viewOf(span: SpanView): TraceView {
  return summed({
    traceId: span.traceId,
    name: span.name,
    start: span.start,
    durationMs: 0,
    live: false,
    error: false,
    services: [],
    seq: 0,
    rootIds: [],
    spans: [],
  }, [span])
}

// the trace as read, with the feed's spans since folded in; the feed's spans alone while no read has it yet
export function withFeedSpans(base: TraceView | undefined, spans: Iterable<SpanView>): TraceView | undefined {
  let view = base
  for (const span of spans) {
    view = view === undefined
      ? viewOf(span)
      : upsertSpan(view, span)
  }
  return view
}
