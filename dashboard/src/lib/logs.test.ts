import { describe, expect, it } from 'vitest'
import { filterError, logQuery, matchesLine, NO_FILTER, severityOf } from './logs'

describe('a log filter', () => {
  it('asks for only what it sets', () => {
    expect(logQuery(NO_FILTER, 'UTC')).toEqual({})
    expect(logQuery({
      device: 'd1',
      source: ' audiod ',
      level: 'warn',
      q: ' timeout ',
      since: '2026-11-12T21:00',
      until: '2026-11-12T22:00',
    }, 'Asia/Bishkek')).toEqual({
      device: 'd1',
      source: 'audiod',
      level: 'warn',
      q: 'timeout',
      since: '2026-11-12T15:00:00.000Z',
      until: '2026-11-12T16:00:00.000Z',
    })
  })

  it('refuses what the server would: an until not after since, a text or source past its bytes', () => {
    expect(filterError({ ...NO_FILTER, since: '2026-11-12T22:00', until: '2026-11-12T22:00' }, 'UTC')).toEqual({
      field: 'until',
      message: 'Until must be after From.',
    })
    expect(filterError({
      ...NO_FILTER,
      q: 'й'.repeat(129),
    }, 'UTC')).toEqual({
      field: 'q',
      message: 'At most 256 bytes.',
    })
    expect(filterError({
      ...NO_FILTER,
      source: 'x'.repeat(65),
    }, 'UTC')).toEqual({
      field: 'source',
      message: 'At most 64 bytes.',
    })
    expect(filterError({
      ...NO_FILTER,
      q: 'й'.repeat(128),
      source: 'x'.repeat(64),
    }, 'UTC')).toBeUndefined()
  })
})

it('reads a severity number as the level a device logs at, a number between them as the one below', () => {
  expect([1, 5, 8, 9, 12, 13, 16, 17, 21, 0].map(severityOf)).toEqual([
    'debug',
    'debug',
    'debug',
    'info',
    'info',
    'warn',
    'warn',
    'error',
    'error',
    'debug',
  ])
})

describe('a live line', () => {
  const line = {
    deviceId: 'd1',
    source: 'cadenced',
    severity: 13,
    body: 'Link LOST code=timeout',
  }

  it('matches the filter as the server reads it', () => {
    expect(matchesLine(line, NO_FILTER)).toBe(true)
    expect(matchesLine(line, {
      ...NO_FILTER,
      device: 'd1',
      source: 'cadenced',
      level: 'warn',
      q: 'link lost',
    })).toBe(true)
    expect(matchesLine(line, { ...NO_FILTER, device: 'd2' })).toBe(false)
    expect(matchesLine(line, { ...NO_FILTER, source: 'audiod' })).toBe(false)
    expect(matchesLine(line, { ...NO_FILTER, level: 'error' })).toBe(false)
    expect(matchesLine(line, { ...NO_FILTER, q: 'found' })).toBe(false)
  })

  it('leaves the server\'s own lines out of a device\'s', () => {
    expect(matchesLine({ ...line, deviceId: undefined }, { ...NO_FILTER, device: 'd1' })).toBe(false)
    expect(matchesLine({ ...line, deviceId: undefined, source: 'server' }, { ...NO_FILTER, source: 'server' })).toBe(true)
  })
})
