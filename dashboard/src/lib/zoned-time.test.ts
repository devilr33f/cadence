import { describe, expect, it } from 'vitest'
import { instantToZoned, zonedToInstant, zoneOffsetMs } from './zoned-time'

describe('a wall time in the household\'s zone', () => {
  it('is the instant that zone reads it at', () => {
    expect(zonedToInstant('2026-11-12T21:04', 'Asia/Bishkek')).toBe('2026-11-12T15:04:00.000Z')
    expect(zonedToInstant('2026-11-12T21:04', 'UTC')).toBe('2026-11-12T21:04:00.000Z')
    expect(zonedToInstant('2026-11-12T21:04', 'Asia/Kolkata')).toBe('2026-11-12T15:34:00.000Z')
  })

  it('follows a zone\'s summer time', () => {
    expect(zonedToInstant('2026-07-01T12:00', 'Europe/Berlin')).toBe('2026-07-01T10:00:00.000Z')
    expect(zonedToInstant('2026-12-01T12:00', 'Europe/Berlin')).toBe('2026-12-01T11:00:00.000Z')
  })

  it('is nothing for an empty or unreadable value', () => {
    expect(zonedToInstant('', 'UTC')).toBeUndefined()
    expect(zonedToInstant('12/11/2026 21:04', 'UTC')).toBeUndefined()
  })

  it('reads back as the same wall time', () => {
    expect(instantToZoned('2026-11-12T15:04:00.000Z', 'Asia/Bishkek')).toBe('2026-11-12T21:04')
  })
})

it('measures a zone\'s offset at an instant', () => {
  expect(zoneOffsetMs(Date.parse('2026-11-12T15:04:00Z'), 'Asia/Bishkek')).toBe(6 * 3600_000)
  expect(zoneOffsetMs(Date.parse('2026-11-12T15:04:00Z'), 'America/New_York')).toBe(-5 * 3600_000)
})
