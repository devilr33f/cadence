import { describe, expect, it } from 'vitest'
import { span } from './fixtures'
import { droppedCount, readSpanEvent } from './span-event'

describe('span events', () => {
  it('reads a traces topic event\'s seq and span', () => {
    const s = span({ spanId: 'a', seq: 7 })
    expect(readSpanEvent({
      type: 'span_end',
      data: { seq: 7, span: s },
    })).toEqual({ type: 'span_end', seq: 7, span: s })
  })

  it('refuses another event or a span without its ids', () => {
    expect(readSpanEvent({
      type: 'log_line',
      data: {
        seq: 1,
        span: span({ spanId: 'a' }),
      },
    })).toBeUndefined()
    expect(readSpanEvent({
      type: 'span_start',
      data: {
        seq: 1,
        span: { traceId: 't' },
      },
    })).toBeUndefined()
    expect(readSpanEvent({
      type: 'span_start',
      data: {
        span: span({ spanId: 'a' }),
      },
    })).toBeUndefined()
    expect(readSpanEvent({
      type: 'span_start',
      data: {
        seq: 1,
        span: {
          ...span({ spanId: 'a' }),
          start: 'soon',
        },
      },
    })).toBeUndefined()
  })

  it('reads the feed\'s count of span events it held back', () => {
    expect(droppedCount({
      type: 'span_dropped',
      data: { count: 12 },
    })).toBe(12)
    expect(droppedCount({
      type: 'span_end',
      data: { count: 12 },
    })).toBeUndefined()
  })
})
