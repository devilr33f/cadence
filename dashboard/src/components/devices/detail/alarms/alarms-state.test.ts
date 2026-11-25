import { describe, expect, it } from 'vitest'
import { kindText, nextUp, parseAlarmsState, repeatText, snoozeOf, stoppedByText } from './alarms-state'

// integrations/home_assistant/tests/fixtures/device_kitchen.json's alarms state
const KITCHEN = {
  alarms: [
    {
      id: 'a1',
      days: ['mon', 'tue', 'wed', 'thu', 'fri'],
      next: '2026-11-13T07:39:00Z',
      time: '07:30',
      level: 40,
      rampSeconds: 120,
      rampFrom: 'quiet',
      limitSeconds: 900,
    },
  ],
  timers: [
    {
      id: 't1',
      next: '2026-11-12T10:10:00Z',
      endsAt: '2026-11-12T10:10:00Z',
      seconds: 600,
    },
  ],
  ringing: null,
  snoozed: [
    { id: 'a1', kind: 'alarm', until: '2026-11-13T07:39:00Z' },
  ],
  lastRing: {
    at: '2026-11-12T10:00:00Z',
    id: 'a1',
    kind: 'alarm',
    stoppedBy: 'snooze',
  },
  reminders: [
    {
      id: 'r1',
      date: '2026-11-12',
      next: '2026-11-12T18:00:00Z',
      time: '18:00',
      label: 'Take the bins out',
    },
  ],
}

describe('parseAlarmsState', () => {
  it('reads every known part of the state', () => {
    const state = parseAlarmsState(KITCHEN)
    expect(state.alarms).toEqual([
      {
        id: 'a1',
        time: '07:30',
        days: ['mon', 'tue', 'wed', 'thu', 'fri'],
        date: undefined,
        level: 40,
        rampSeconds: 120,
        rampFrom: 'quiet',
        limitSeconds: 900,
        next: '2026-11-13T07:39:00Z',
      },
    ])
    expect(state.timers).toEqual([
      { id: 't1', seconds: 600, endsAt: '2026-11-12T10:10:00Z' },
    ])
    expect(state.reminders).toEqual([
      {
        id: 'r1',
        time: '18:00',
        days: [],
        date: '2026-11-12',
        label: 'Take the bins out',
        next: '2026-11-12T18:00:00Z',
      },
    ])
    expect(state.others).toEqual([])
    expect(state.ringing).toBeNull()
    expect(state.snoozed).toEqual([
      { kind: 'alarm', id: 'a1', until: '2026-11-13T07:39:00Z' },
    ])
    expect(state.lastRing).toEqual({
      kind: 'alarm',
      id: 'a1',
      at: '2026-11-12T10:00:00Z',
      stoppedBy: 'snooze',
    })
  })

  it('takes a state without reminders, and a ring', () => {
    const state = parseAlarmsState({
      alarms: [],
      timers: [],
      ringing: { kind: 'timer', id: 't1' },
    })
    expect(state.reminders).toEqual([])
    expect(state.ringing).toEqual({ kind: 'timer', id: 't1' })
    expect(state.lastRing).toBeNull()
  })

  it('reads a one-shot reminder\'s instant and level', () => {
    const state = parseAlarmsState({
      reminders: [
        {
          id: 'r2',
          label: 'Stretch',
          at: '2026-11-12T10:20:00Z',
          next: '2026-11-12T10:20:00Z',
          level: 30,
        },
      ],
    })
    expect(state.reminders).toEqual([
      {
        id: 'r2',
        time: undefined,
        days: [],
        date: undefined,
        at: '2026-11-12T10:20:00Z',
        label: 'Stretch',
        level: 30,
        next: '2026-11-12T10:20:00Z',
      },
    ])
  })

  it('keeps a list of a newer kind as generic items, and drops malformed entries', () => {
    const state = parseAlarmsState({
      alarms: [
        { id: 'a1' },
        'junk',
        { id: 'a2', time: '06:00' },
      ],
      timers: [
        { id: 't1' },
      ],
      ringing: { kind: 'alarm' },
      snoozed: [
        { kind: 'alarm', id: 'a2' },
      ],
      lastRing: { kind: 'alarm', id: 'a2' },
      wakeups: [
        { id: 'w1', label: 'Sunrise', next: '2026-11-13T06:00:00Z' },
        { label: 'no id' },
      ],
      empties: [],
      note: 'not a list',
    })
    expect(state.alarms.map(a => a.id)).toEqual(['a2'])
    expect(state.timers).toEqual([])
    expect(state.ringing).toBeNull()
    expect(state.snoozed).toEqual([])
    expect(state.lastRing).toBeNull()
    expect(state.others).toEqual([
      {
        kind: 'wakeup',
        items: [
          { id: 'w1', label: 'Sunrise', next: '2026-11-13T06:00:00Z' },
        ],
      },
    ])
  })

  it.each([null, undefined, 'x', 3, []])('reads nothing from %j', (value) => {
    expect(parseAlarmsState(value)).toEqual({
      alarms: [],
      timers: [],
      reminders: [],
      others: [],
      ringing: null,
      snoozed: [],
      lastRing: null,
    })
  })
})

describe('nextUp', () => {
  it('is the soonest of every kind', () => {
    expect(nextUp(parseAlarmsState(KITCHEN))).toEqual({ kind: 'timer', id: 't1', at: '2026-11-12T10:10:00Z' })
    expect(nextUp(parseAlarmsState({ ...KITCHEN, timers: [] }))).toEqual({
      kind: 'reminder',
      id: 'r1',
      at: '2026-11-12T18:00:00Z',
      label: 'Take the bins out',
    })
    expect(nextUp(parseAlarmsState({
      wakeups: [
        { id: 'w1', next: '2026-11-01T06:00:00Z' },
      ],
      ...KITCHEN,
    }))).toEqual({
      kind: 'wakeup',
      id: 'w1',
      at: '2026-11-01T06:00:00Z',
      label: undefined,
    })
  })

  it('is nothing while nothing is set', () => {
    expect(nextUp(parseAlarmsState({
      alarms: [
        { id: 'a1', time: '07:00' },
      ],
      timers: [],
    }))).toBeUndefined()
  })
})

it('finds an entry\'s snooze by its kind and id', () => {
  const state = parseAlarmsState(KITCHEN)
  expect(snoozeOf(state, 'alarm', 'a1')?.until).toBe('2026-11-13T07:39:00Z')
  expect(snoozeOf(state, 'timer', 'a1')).toBeUndefined()
})

it.each<[string[], string | undefined, string]>([
  [
    [
      'mon',
      'tue',
      'wed',
      'thu',
      'fri',
      'sat',
      'sun',
    ],
    undefined,
    'Every day',
  ],
  [
    ['fri', 'thu', 'wed', 'tue', 'mon'],
    undefined,
    'Weekdays',
  ],
  [
    ['sun', 'sat'],
    undefined,
    'Weekends',
  ],
  [
    ['wed', 'mon'],
    undefined,
    'Mon, Wed',
  ],
  [[], '2026-11-12', '12 Nov 2026'],
  [[], 'garbage', 'garbage'],
  [[], undefined, 'Once'],
])('words days %j and date %s as %s', (days, date, expected) => {
  expect(repeatText(days, date)).toBe(expected)
})

it('words how a ring stopped, and a code it does not know as it is', () => {
  expect(stoppedByText('button')).toBe('by a button')
  expect(stoppedByText('remote')).toBe('remotely')
  expect(stoppedByText('laser')).toBe('laser')
  expect(stoppedByText('')).toBe('unknown')
  expect(kindText('reminder')).toBe('Reminder')
})
