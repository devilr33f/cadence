import type { SpanView } from '@/api/gen'

// the server zeroes a stream's speeds after this long without bytes; the page does too between its updates
export const STREAM_STALE_MS = 3000
// the streams a panel follows at most
export const STREAMS_LIMIT = 50

export interface StreamRow {
  span: SpanView
  bytesIn: number
  bytesOut: number
  // bytes a second as of now: zero once the stream went quiet
  speedIn: number
  speedOut: number
  // when the page last heard of it (ms)
  heardAt: number
}

interface Held {
  span: SpanView
  heardAt: number
}

// the live spans that move bytes: each span_update replaces its counters, its end lets it go
export class StreamBook {
  #held = new Map<string, Held>()

  // false when it changed nothing the panel shows
  span(span: SpanView, now: number): boolean {
    if (!span.live || !span.stream) {
      return this.#held.delete(span.spanId)
    }
    const old = this.#held.get(span.spanId)
    if (old && old.span.seq > span.seq)
      return false
    this.#held.set(span.spanId, { span, heardAt: now })
    if (this.#held.size > STREAMS_LIMIT) {
      const oldest = [
        ...this.#held.entries(),
      ].sort((a, b) => a[1].heardAt - b[1].heardAt)[0]
      if (oldest)
        this.#held.delete(oldest[0])
    }
    return true
  }

  get size(): number {
    return this.#held.size
  }

  // the busiest first, then the newest
  rows(now: number): StreamRow[] {
    return [
      ...this.#held.values(),
    ]
      .map(({ span, heardAt }) => streamRow(span, heardAt, now))
      .sort((a, b) => (b.speedIn + b.speedOut) - (a.speedIn + a.speedOut) || Date.parse(b.span.start) - Date.parse(a.span.start))
  }
}

export function streamRow(span: SpanView, heardAt: number, now: number): StreamRow {
  const stream = span.stream ?? {
    bytesIn: 0,
    bytesOut: 0,
    speedIn: 0,
    speedOut: 0,
  }
  const stale = now - heardAt > STREAM_STALE_MS
  return {
    span,
    bytesIn: stream.bytesIn,
    bytesOut: stream.bytesOut,
    speedIn: stale ? 0 : stream.speedIn,
    speedOut: stale ? 0 : stream.speedOut,
    heardAt,
  }
}

// every stream's bytes and current speeds together
export function streamTotals(rows: readonly StreamRow[]) {
  return rows.reduce((sum, r) => ({
    bytesIn: sum.bytesIn + r.bytesIn,
    bytesOut: sum.bytesOut + r.bytesOut,
    speedIn: sum.speedIn + r.speedIn,
    speedOut: sum.speedOut + r.speedOut,
  }), {
    bytesIn: 0,
    bytesOut: 0,
    speedIn: 0,
    speedOut: 0,
  })
}
