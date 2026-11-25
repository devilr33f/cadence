import { describe, expect, it } from 'vitest'
import { TraceBook } from './book'
import { at, span, summary } from './fixtures'

describe('trace book', () => {
  it('lists a page newest first and replaces it with the next newest page', () => {
    const book = new TraceBook()
    book.page([
      summary({
        traceId: 'a',
        start: at(0),
      }),
      summary({
        traceId: 'b',
        start: at(500),
      }),
    ], 9, true)
    expect(book.rows().map(r => r.traceId)).toEqual(['b', 'a'])
    expect(book.readSeq).toBe(9)
    book.page([
      summary({ traceId: 'c' }),
    ], 12, true)
    expect(book.rows().map(r => r.traceId)).toEqual(['c'])
    expect(book.readSeq).toBe(12)
  })

  it('builds a trace the feed tells of first from its spans', () => {
    const book = new TraceBook()
    book.span({
      type: 'span_start',
      seq: 3,
      span: span({
        traceId: 'n',
        spanId: 'root',
        name: 'voice.turn',
        live: true,
        durationMs: 0,
        seq: 3,
      }),
    })
    book.span({
      type: 'span_end',
      seq: 4,
      span: span({
        traceId: 'n',
        spanId: 'child',
        parentId: 'root',
        name: 'stt',
        service: 'cadence-device',
        deviceId: 'd1',
        start: at(20),
        durationMs: 50,
        status: 'error',
        seq: 4,
      }),
    })
    const [row] = book.rows()
    expect(row).toMatchObject({
      traceId: 'n',
      name: 'voice.turn',
      live: true,
      error: true,
      spans: 2,
      services: ['cadence-device', 'cadence-server'],
      deviceId: 'd1',
      durationMs: 70,
      seq: 4,
    })
  })

  it('counts a span the feed saw begin after the summary, not one it only saw end', () => {
    const book = new TraceBook()
    book.page([
      summary({
        traceId: 't1',
        spans: 2,
        live: true,
        seq: 5,
      }),
    ], 5, true)
    book.span({
      type: 'span_end',
      seq: 6,
      span: span({ spanId: 'old', seq: 6 }),
    })
    expect(book.rows()[0]!.spans).toBe(2)
    book.span({
      type: 'span_start',
      seq: 7,
      span: span({ spanId: 'new', live: true, seq: 7 }),
    })
    expect(book.rows()[0]!.spans).toBe(3)
    // an update of the same span later counts it once
    book.span({
      type: 'span_update',
      seq: 8,
      span: span({ spanId: 'new', live: true, seq: 8 }),
    })
    expect(book.rows()[0]!.spans).toBe(3)
  })

  it('ignores what a summary already holds and a span copy older than the one held', () => {
    const book = new TraceBook()
    book.page([
      summary({ traceId: 't1', seq: 5 }),
    ], 5, true)
    expect(book.span({
      type: 'span_end',
      seq: 4,
      span: span({ spanId: 's', seq: 4 }),
    })).toBe(false)
    expect(book.span({
      type: 'span_start',
      seq: 6,
      span: span({ spanId: 's', live: true, seq: 6 }),
    })).toBe(true)
    expect(book.span({
      type: 'span_start',
      seq: 6,
      span: span({ spanId: 's', live: true, seq: 6 }),
    })).toBe(false)
    expect(book.lastSeq).toBe(6)
  })

  it('lets a later summary settle what the feed said, and keeps newer spans over it', () => {
    const book = new TraceBook()
    book.page([
      summary({
        traceId: 't1',
        spans: 1,
        live: true,
        seq: 2,
      }),
    ], 2, true)
    book.span({
      type: 'span_start',
      seq: 3,
      span: span({ spanId: 'a', live: true, seq: 3 }),
    })
    book.span({
      type: 'span_start',
      seq: 5,
      span: span({ spanId: 'b', live: true, seq: 5 }),
    })
    book.page([
      summary({
        traceId: 't1',
        spans: 2,
        live: true,
        seq: 4,
      }),
    ], 4, false)
    expect(book.rows()[0]!.spans).toBe(3)
    book.page([
      summary({
        traceId: 't1',
        spans: 3,
        live: false,
        durationMs: 40,
        seq: 9,
      }),
    ], 9, false)
    expect(book.rows()[0]).toMatchObject({ spans: 3, live: false, durationMs: 40 })
    // an older summary never replaces a newer one
    book.page([
      summary({ traceId: 't1', spans: 1, seq: 3 }),
    ], 9, false)
    expect(book.rows()[0]!.spans).toBe(3)
    expect(book.readSeq).toBe(9)
  })

  it('lets the oldest finished traces go past its limit, never a live one', () => {
    const book = new TraceBook(2)
    book.page([
      summary({
        traceId: 'old-live',
        start: at(0),
        live: true,
      }),
      summary({
        traceId: 'old',
        start: at(10),
      }),
      summary({
        traceId: 'new',
        start: at(20),
      }),
    ], 3, true)
    expect(book.rows().map(r => r.traceId)).toEqual(['new', 'old-live'])
    expect(book.has('old')).toBe(false)
  })
})
