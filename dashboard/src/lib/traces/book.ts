import type { SpanView, TraceSummary } from '@/api/gen'

// the traces a list keeps at most: the newest by start, a live one never let go first
export const BOOK_LIMIT = 300

// a span the feed told of since the trace's last summary, as much of it as a row needs
interface SeenSpan {
  seq: number
  live: boolean
  start: number
  end: number
  name: string
  parentId?: string
  service: string
  error: boolean
  deviceId?: string
  // the feed saw it begin, so no summary read before that counted it
  fresh: boolean
}

interface Entry {
  traceId: string
  summary?: TraceSummary
  summarySeq: number
  seen: Map<string, SeenSpan>
}

// one row of the list: the server's summary with what the feed said since folded in
export interface TraceRow {
  traceId: string
  name: string
  start: number
  startIso: string
  // while live, the time it had run as of the last word on it: the page ticks it on from start
  durationMs: number
  live: boolean
  error: boolean
  spans: number
  services: string[]
  deviceId?: string
  seq: number
}

export interface SpanEventInput {
  type: string
  seq: number
  span: SpanView
}

function seenOf(type: string, span: SpanView, old: SeenSpan | undefined): SeenSpan {
  const start = Date.parse(span.start)
  return {
    seq: span.seq,
    live: span.live,
    start,
    end: start + Math.max(0, span.durationMs),
    name: span.name,
    ...(span.parentId && { parentId: span.parentId }),
    service: span.service,
    error: span.status === 'error',
    ...(span.deviceId && { deviceId: span.deviceId }),
    fresh: old?.fresh ?? type === 'span_start',
  }
}

// the row a summary and the spans seen after it make together
function rowOf(entry: Entry): TraceRow | undefined {
  const { summary } = entry
  const overlay = [
    ...entry.seen.values(),
  ].filter(s => s.seq > entry.summarySeq)
  if (!summary && overlay.length === 0)
    return undefined
  let start = summary
    ? Date.parse(summary.start)
    : Number.POSITIVE_INFINITY
  let end = summary
    ? start + summary.durationMs
    : Number.NEGATIVE_INFINITY
  const services = new Set(summary?.services ?? [])
  let error = summary?.error ?? false
  let live = summary?.live ?? false
  let deviceId = summary?.deviceId || undefined
  for (const s of overlay) {
    start = Math.min(start, s.start)
    end = Math.max(end, s.end)
    if (s.service)
      services.add(s.service)
    error ||= s.error
    live ||= s.live
    deviceId ??= s.deviceId
  }
  let name = summary?.name ?? ''
  if (!summary) {
    // the earliest span whose parent is not in the trace, as the server names it
    const top = overlay
      .filter(s => s.parentId === undefined || !entry.seen.has(s.parentId))
      .sort((a, b) => a.start - b.start)[0]
    name = top?.name ?? overlay[0]?.name ?? ''
  }
  const added = overlay.filter(s => summary ? s.fresh : true).length
  return {
    traceId: entry.traceId,
    name,
    start,
    startIso: new Date(start).toISOString(),
    durationMs: Math.max(0, end - start),
    live,
    error,
    spans: (summary?.spans ?? 0) + added,
    services: [...services].sort(),
    ...(deviceId && { deviceId }),
    seq: Math.max(entry.summarySeq, ...overlay.map(s => s.seq)),
  }
}

// the list page's traces: summaries read from the server, kept current by the feed's span events in between. a summary
// read after an event says how it settled (a span ended, a device's spans arrived), so the page reads the traces
// changed since its last read (`since`) shortly after events and after every gap
export class TraceBook {
  #entries = new Map<string, Entry>()
  #limit: number
  // the seq of the last list read: the next since read starts there
  readSeq = 0
  // the newest seq any read or event carried
  lastSeq = 0

  constructor(limit = BOOK_LIMIT) {
    this.#limit = limit
  }

  #entry(traceId: string): Entry {
    let entry = this.#entries.get(traceId)
    if (!entry) {
      entry = {
        traceId,
        summarySeq: 0,
        seen: new Map(),
      }
      this.#entries.set(traceId, entry)
    }
    return entry
  }

  has(traceId: string): boolean {
    return this.#entries.has(traceId)
  }

  // a list read: the newest page replaces everything, a since read updates what it names
  page(traces: readonly TraceSummary[], seq: number, replace: boolean) {
    if (replace)
      this.#entries.clear()
    for (const summary of traces) {
      const entry = this.#entry(summary.traceId)
      if (summary.seq < entry.summarySeq)
        continue
      entry.summary = summary
      entry.summarySeq = summary.seq
      for (const [id, s] of entry.seen) {
        if (s.seq <= summary.seq)
          entry.seen.delete(id)
      }
    }
    this.readSeq = replace
      ? seq
      : Math.max(this.readSeq, seq)
    this.lastSeq = Math.max(this.lastSeq, seq)
    this.#trim()
  }

  // a span event; false when it told nothing new (an older copy of a span already held)
  span({ type, seq, span }: SpanEventInput): boolean {
    this.lastSeq = Math.max(this.lastSeq, seq)
    const entry = this.#entry(span.traceId)
    if (span.seq <= entry.summarySeq)
      return false
    const old = entry.seen.get(span.spanId)
    if (old && old.seq >= span.seq)
      return false
    entry.seen.set(span.spanId, seenOf(type, span, old))
    this.#trim()
    return true
  }

  // the rows, newest start first
  rows(): TraceRow[] {
    const rows: TraceRow[] = []
    for (const entry of this.#entries.values()) {
      const row = rowOf(entry)
      if (row)
        rows.push(row)
    }
    return rows.sort((a, b) => b.start - a.start)
  }

  // past the limit the oldest finished traces go first
  #trim() {
    if (this.#entries.size <= this.#limit)
      return
    const rows = this.rows()
    let over = this.#entries.size - this.#limit
    for (let i = rows.length - 1; i >= 0 && over > 0; i--) {
      if (rows[i]!.live)
        continue
      this.#entries.delete(rows[i]!.traceId)
      over--
    }
  }
}
