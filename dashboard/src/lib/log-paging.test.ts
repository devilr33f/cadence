import type { LogView } from '@/api/gen'
import { describe, expect, it } from 'vitest'
import { justAfter, nextLogPage, unseenLines } from './log-paging'

function line(ts: string, body: string): LogView {
  return {
    ts,
    observedTs: ts,
    deviceId: 'd1',
    source: 'cadenced',
    severity: 9,
    body,
    truncated: false,
    continued: false,
  }
}

describe('the time just after a line', () => {
  it('is a microsecond later, as the store keeps its times', () => {
    expect(justAfter('2026-11-12T15:04:12.123456Z')).toBe('2026-11-12T15:04:12.123457Z')
    expect(justAfter('2026-11-12T15:04:12.5Z')).toBe('2026-11-12T15:04:12.500001Z')
    expect(justAfter('2026-11-12T15:04:12Z')).toBe('2026-11-12T15:04:12.000001Z')
  })

  it('carries into the next second, minute and day', () => {
    expect(justAfter('2026-11-12T23:59:59.999999Z')).toBe('2026-11-13T00:00:00.000000Z')
  })

  it('reads an offset, and answers in utc', () => {
    expect(justAfter('2026-11-12T21:04:12.25+06:00')).toBe('2026-11-12T15:04:12.250001Z')
  })

  it('is nothing for a time it cannot read', () => {
    expect(justAfter('yesterday')).toBeUndefined()
  })
})

describe('the next page of stored lines', () => {
  const full = [
    line('2026-11-12T15:04:13Z', 'c'),
    line('2026-11-12T15:04:12.5Z', 'b'),
    line('2026-11-12T15:04:12.5Z', 'a'),
  ]

  it('asks up to just after the oldest line, naming the lines already shown at its time', () => {
    expect(nextLogPage([
      { lines: full, raw: 3 },
    ], 3)).toEqual({
      until: '2026-11-12T15:04:12.500001Z',
      boundary: '2026-11-12T15:04:12.5Z',
      seen: [
        expect.any(String),
        expect.any(String),
      ],
    })
  })

  it('ends when a page came back short', () => {
    expect(nextLogPage([
      { lines: full, raw: 3 },
    ], 200)).toBeUndefined()
  })

  it('skips past a time whose lines fill a page alone, rather than ask for it forever', () => {
    const tied = [
      line('2026-11-12T15:04:12.5Z', 'b'),
      line('2026-11-12T15:04:12.5Z', 'a'),
    ]
    const first = nextLogPage([
      { lines: tied, raw: 2 },
    ], 2)!
    // the same two lines again: nothing new at that time, so the next ask is before it
    expect(nextLogPage([
      { lines: tied, raw: 2 },
      { lines: [], raw: 2, param: first },
    ], 2)).toEqual({
      until: '2026-11-12T15:04:12.5Z',
    })
  })
})

describe('a page\'s new lines', () => {
  it('leave out the lines at the boundary that an earlier page showed, once each', () => {
    const shown = [
      line('2026-11-12T15:04:12.5Z', 'a'),
      line('2026-11-12T15:04:12.5Z', 'a'),
    ]
    const param = nextLogPage([
      { lines: shown, raw: 2 },
    ], 2)!
    const page = [
      line('2026-11-12T15:04:12.5Z', 'a'),
      line('2026-11-12T15:04:12.5Z', 'b'),
      line('2026-11-12T15:04:12.5Z', 'a'),
      line('2026-11-12T15:04:12.5Z', 'a'),
      line('2026-11-12T15:04:12.4Z', 'a'),
    ]
    expect(unseenLines(page, param).map(l => [l.ts, l.body])).toEqual([
      ['2026-11-12T15:04:12.5Z', 'b'],
      ['2026-11-12T15:04:12.5Z', 'a'],
      ['2026-11-12T15:04:12.4Z', 'a'],
    ])
  })

  it('are the whole page for the first one', () => {
    const page = [
      line('2026-11-12T15:04:12.5Z', 'a'),
    ]
    expect(unseenLines(page, {})).toEqual(page)
  })
})
