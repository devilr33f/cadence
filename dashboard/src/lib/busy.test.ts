import { expect, it } from 'vitest'
import { DND, NOT_BUSY } from '@/test/subscriptions'
import { busySummary, sourceText } from './busy'

it('sums up who is busy for the header, in full for a screen reader', () => {
  expect(busySummary(undefined)).toEqual({ short: 'Not busy', full: 'Busy mode: no speaker is busy' })
  expect(busySummary(NOT_BUSY).short).toBe('Not busy')
  expect(busySummary(DND)).toEqual({ short: 'Do not disturb', full: 'Busy mode: do not disturb, 2 speakers holding updates' })
  expect(busySummary({
    signals: [
      {
        source: 'ha:x',
        reason: 'call',
        devices: null,
        expiresAt: null,
      },
    ],
    devices: [
      { deviceId: 'd', reason: 'call' },
    ],
  }))
    .toEqual({ short: 'Busy', full: 'Busy mode: 1 speaker busy, holding updates' })
})

it('words a signal\'s source', () => {
  expect([
    'dashboard',
    'voice',
    'ha:binary_sensor.x',
    'meet-bridge',
  ].map(sourceText)).toEqual([
    'Dashboard',
    'Voice',
    'Home Assistant',
    'API',
  ])
})
