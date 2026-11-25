import type { SpanView } from '@/api/gen'
import type { LiveEvent } from '@/lib/live/frames'
import { count, record } from '@/lib/live/data'

export const SPAN_TYPES: ReadonlySet<string> = new Set(['span_start', 'span_update', 'span_end'])
export const SPAN_DROPPED = 'span_dropped'

export interface SpanEvent {
  type: 'span_start' | 'span_update' | 'span_end'
  seq: number
  span: SpanView
}

// a traces topic event as the page folds it: the change's seq and the whole span, or nothing when it is not one
export function readSpanEvent(event: LiveEvent): SpanEvent | undefined {
  if (!SPAN_TYPES.has(event.type))
    return undefined
  const seq = count(event.data, 'seq')
  const span = record(event.data, 'span')
  if (seq === undefined || !span)
    return undefined
  const { traceId, spanId, start, seq: spanSeq } = span
  if (typeof traceId !== 'string' || typeof spanId !== 'string' || typeof start !== 'string' || traceId === '' || spanId === '')
    return undefined
  if (Number.isNaN(Date.parse(start)))
    return undefined
  return {
    type: event.type as SpanEvent['type'],
    seq,
    span: {
      ...(span as unknown as SpanView),
      seq: typeof spanSeq === 'number'
        ? spanSeq
        : seq,
      durationMs: typeof span.durationMs === 'number'
        ? span.durationMs
        : 0,
      live: span.live === true,
      service: typeof span.service === 'string'
        ? span.service
        : '',
      name: typeof span.name === 'string'
        ? span.name
        : '',
    },
  }
}

// how many span events the feed held back past its 200 a second
export function droppedCount(event: LiveEvent): number | undefined {
  return event.type === SPAN_DROPPED
    ? count(event.data, 'count')
    : undefined
}
