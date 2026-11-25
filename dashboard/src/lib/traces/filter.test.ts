import { describe, expect, it } from 'vitest'
import { TraceBook } from './book'
import { filterRows, NO_TRACE_FILTER } from './filter'
import { at, summary } from './fixtures'

function rows() {
  const book = new TraceBook()
  book.page([
    summary({
      traceId: 'a',
      name: 'voice.turn',
      live: true,
      start: at(3),
    }),
    summary({
      traceId: 'b',
      name: 'GET /api/v1/devices',
      error: true,
      start: at(2),
    }),
    summary({
      traceId: 'c',
      name: 'Voice.Turn',
      error: true,
      start: at(1),
    }),
  ], 1, true)
  return book.rows()
}

describe('trace filter', () => {
  it('keeps every row without a filter', () => {
    const all = rows()
    expect(filterRows(all, NO_TRACE_FILTER)).toBe(all)
  })

  it('narrows by running, errors and a name in any case', () => {
    const ids = (f: Partial<typeof NO_TRACE_FILTER>) => filterRows(rows(), {
      ...NO_TRACE_FILTER,
      ...f,
    }).map(r => r.traceId)
    expect(ids({ liveOnly: true })).toEqual(['a'])
    expect(ids({ errorsOnly: true })).toEqual(['b', 'c'])
    expect(ids({ name: ' voice ' })).toEqual(['a', 'c'])
    expect(ids({ name: 'voice', errorsOnly: true })).toEqual(['c'])
  })
})
