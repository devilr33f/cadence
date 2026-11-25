import { describe, expect, it } from 'vitest'
import { span } from './fixtures'
import { STREAM_STALE_MS, StreamBook, streamTotals } from './streams'

function stream(bytesIn: number, speedIn: number) {
  return {
    bytesIn,
    bytesOut: 0,
    speedIn,
    speedOut: 0,
  }
}

describe('stream book', () => {
  it('follows live spans with counters, busiest first, and lets one go at its end', () => {
    const book = new StreamBook()
    expect(book.span(span({ spanId: 'plain', live: true, seq: 1 }), 0)).toBe(false)
    expect(book.span(span({
      spanId: 'a',
      live: true,
      seq: 1,
      stream: stream(100, 10),
    }), 0)).toBe(true)
    expect(book.span(span({
      spanId: 'b',
      live: true,
      seq: 1,
      stream: stream(100, 50),
    }), 0)).toBe(true)
    expect(book.rows(0).map(r => r.span.spanId)).toEqual(['b', 'a'])
    expect(book.span(span({
      spanId: 'b',
      seq: 2,
      stream: stream(200, 0),
    }), 0)).toBe(true)
    expect(book.rows(0).map(r => r.span.spanId)).toEqual(['a'])
    expect(book.size).toBe(1)
  })

  it('keeps the newer copy and zeroes a quiet stream\'s speeds', () => {
    const book = new StreamBook()
    book.span(span({
      spanId: 'a',
      live: true,
      seq: 5,
      stream: stream(500, 40),
    }), 1000)
    expect(book.span(span({
      spanId: 'a',
      live: true,
      seq: 4,
      stream: stream(100, 1),
    }), 1000)).toBe(false)
    expect(book.rows(1000)[0]).toMatchObject({ bytesIn: 500, speedIn: 40 })
    expect(book.rows(1000 + STREAM_STALE_MS + 1)[0]).toMatchObject({ bytesIn: 500, speedIn: 0 })
  })

  it('adds every stream up', () => {
    const book = new StreamBook()
    book.span(span({
      spanId: 'a',
      live: true,
      stream: {
        bytesIn: 1,
        bytesOut: 2,
        speedIn: 3,
        speedOut: 4,
      },
    }), 0)
    book.span(span({
      spanId: 'b',
      live: true,
      stream: {
        bytesIn: 10,
        bytesOut: 20,
        speedIn: 30,
        speedOut: 40,
      },
    }), 0)
    expect(streamTotals(book.rows(0))).toEqual({
      bytesIn: 11,
      bytesOut: 22,
      speedIn: 33,
      speedOut: 44,
    })
  })
})
