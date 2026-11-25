import { expect, it } from 'vitest'
import { durationText, formatClock, STAMP_PLACEHOLDER, stampText } from './format-time'

it('words a time in the zone it is given, whatever zone the machine runs in', () => {
  expect(formatClock('2026-11-12T15:04:12Z', 'Asia/Bishkek')).toBe('21:04:12')
  expect(formatClock('2026-11-12T15:04:12Z', 'UTC')).toBe('15:04:12')
  expect(formatClock('2026-11-12T15:04:12.087Z', 'UTC', true)).toBe('15:04:12.087')
})

it('words a day and a minute in the household\'s zone, and a placeholder until it is known', () => {
  expect(stampText('2026-11-12T20:04:12Z', 'Asia/Bishkek')).toBe('13 Nov, 02:04')
  expect(stampText('2026-11-12T20:04:12Z', null)).toBe(STAMP_PLACEHOLDER)
  expect(stampText('2026-11-12T20:04:12Z', 'Asia/Bishkek', true)).toBe('13 Nov, 02:04:12')
  expect(stampText('2026-11-12T20:04:12Z', null, true)).toBe(`${STAMP_PLACEHOLDER}:--`)
})

it('words a length of media as a player does', () => {
  expect(durationText(0)).toBe('0:00')
  expect(durationText(161_400)).toBe('2:41')
  expect(durationText(3_725_000)).toBe('1:02:05')
})
