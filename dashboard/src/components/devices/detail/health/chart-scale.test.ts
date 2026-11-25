import { describe, expect, it } from 'vitest'
import { niceDomain, niceTicks, timeTicks } from './chart-scale'

describe('a value axis', () => {
  it('ticks at round steps of 1, 2 or 5', () => {
    expect(niceTicks(0, 1)).toEqual([0, 0.5, 1])
    expect(niceTicks(0, 100)).toEqual([0, 50, 100])
    expect(niceTicks(38, 52)).toEqual([40, 45, 50])
    expect(niceTicks(0, 0)).toEqual([0])
  })

  it('reaches past the values to round ends, with room around a flat line', () => {
    expect(niceDomain(41.2, 47.8)).toEqual([40, 50])
    expect(niceDomain(45, 45)).toEqual([44, 46])
  })
})

describe('a time axis', () => {
  const end = Date.parse('2026-11-12T15:07:00Z')

  it('ticks on the household\'s clock at a step that fits the window', () => {
    // an hour: every 15 minutes, on the quarter hours of Bishkek (utc+6)
    expect(timeTicks(end - 3600_000, end, 'Asia/Bishkek').map(t => new Date(t).toISOString())).toEqual([
      '2026-11-12T14:15:00.000Z',
      '2026-11-12T14:30:00.000Z',
      '2026-11-12T14:45:00.000Z',
      '2026-11-12T15:00:00.000Z',
    ])
    // a day: every 6 hours of the household's day, which Kolkata (utc+5:30) starts at 18:30 utc
    expect(timeTicks(end - 86_400_000, end, 'Asia/Kolkata').map(t => new Date(t).toISOString())).toEqual([
      '2026-11-11T18:30:00.000Z',
      '2026-11-12T00:30:00.000Z',
      '2026-11-12T06:30:00.000Z',
      '2026-11-12T12:30:00.000Z',
    ])
  })

  it('ticks every 5 minutes over a quarter hour, every 2 hours over six', () => {
    expect(timeTicks(end - 900_000, end, 'UTC')).toHaveLength(3)
    expect(timeTicks(end - 6 * 3600_000, end, 'UTC')).toHaveLength(3)
  })
})
