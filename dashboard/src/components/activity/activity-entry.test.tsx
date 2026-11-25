import { describe, expect, it } from 'vitest'
import { activityEntry } from './activity-entry'

function turnStart(trigger?: string) {
  return activityEntry({
    seq: 1,
    at: '2026-11-12T15:04:12Z',
    type: 'turn_start',
    ...(trigger !== undefined && {
      data: { trigger },
    }),
  })?.text
}

describe('a turn\'s start', () => {
  it.each([
    ['wake', 'Woken by voice'],
    ['button', 'Woken by a press'],
    ['follow_up', 'Follow-up'],
    ['event', 'Event turn'],
    ['dashboard', 'Typed in the dashboard'],
  ])('names what started it, as the feed publishes %s', (trigger, text) => {
    expect(turnStart(trigger)).toBe(text)
  })

  it('says only that a turn began when the trigger is missing or unknown', () => {
    expect(turnStart()).toBe('Turn began')
    expect(turnStart('telepathy')).toBe('Turn began')
    // the proto's spelling, which only a server before v2's published
    expect(turnStart('TRIGGER_FOLLOW_UP')).toBe('Turn began')
    expect(turnStart('TRIGGER_UNSPECIFIED')).toBe('Turn began')
  })
})
