import { describe, expect, it } from 'vitest'
import { durationSeconds, reminderArgs, timerArgs, validateReminder, validateTimer } from './alarm-form'

const timer = {
  id: 'tea',
  duration: { hours: '1', minutes: '1', seconds: '1' },
}

const reminder = {
  id: 'r1',
  label: 'Take the bins out',
  mode: 'after' as const,
  duration: { hours: '0', minutes: '10', seconds: '0' },
  time: '18:00',
  repeat: 'once' as const,
  date: '',
  days: [],
}

describe('timer arguments', () => {
  it('reads hours, minutes and seconds as the device\'s total seconds', () => {
    expect(durationSeconds(timer.duration)).toBe(3661)
    expect(validateTimer(timer)).toEqual({})
    expect(timerArgs(timer)).toEqual({ id: 'tea', seconds: 3661 })
  })

  it.each([
    '',
    'Tea',
    'two words',
    'a'.repeat(17),
  ])('refuses id %j', (id) => {
    expect(validateTimer({ ...timer, id }).id).toBe('Use 1–16 lowercase letters or digits, with no spaces.')
  })

  it.each([
    { hours: '0', minutes: '0', seconds: '0' },
    { hours: '24', minutes: '0', seconds: '1' },
    { hours: '-1', minutes: '0', seconds: '0' },
    { hours: '0', minutes: '1.5', seconds: '0' },
    { hours: '99999999999999999999', minutes: '0', seconds: '0' },
  ])('refuses a duration outside the device\'s rules: %j', (duration) => {
    expect(validateTimer({ ...timer, duration }).duration).toBeTruthy()
  })
})

describe('reminder arguments', () => {
  it('sends a delay as inSeconds, with no time, days, date, session or level', () => {
    expect(validateReminder(reminder)).toEqual({})
    expect(reminderArgs(reminder)).toEqual({ id: 'r1', text: 'Take the bins out', inSeconds: 600 })
  })

  it('sends a time alone for the next occurrence', () => {
    const values = { ...reminder, mode: 'clock' as const }
    expect(validateReminder(values)).toEqual({})
    expect(reminderArgs(values)).toEqual({ id: 'r1', text: 'Take the bins out', time: '18:00' })
  })

  it('sends a real date, or weekdays in the device\'s order', () => {
    const date = {
      ...reminder,
      mode: 'clock' as const,
      repeat: 'date' as const,
      date: '2028-02-29',
    }
    expect(validateReminder(date)).toEqual({})
    expect(reminderArgs(date)).toEqual({
      id: 'r1',
      text: 'Take the bins out',
      time: '18:00',
      date: '2028-02-29',
    })
    const days = {
      ...date,
      repeat: 'days' as const,
      days: ['fri', 'mon', 'wed'],
    }
    expect(validateReminder(days)).toEqual({})
    expect(reminderArgs(days)).toEqual({
      id: 'r1',
      text: 'Take the bins out',
      time: '18:00',
      days: ['mon', 'wed', 'fri'],
    })
  })

  it('refuses an invalid time, calendar date, empty weekday set and delay over a week', () => {
    expect(validateReminder({ ...reminder, mode: 'clock', time: '24:00' }).time).toBeTruthy()
    expect(validateReminder({
      ...reminder,
      mode: 'clock',
      repeat: 'date',
      date: '2027-02-29',
    }).date).toBeTruthy()
    expect(validateReminder({
      ...reminder,
      mode: 'clock',
      repeat: 'days',
      days: [],
    }).days).toBeTruthy()
    expect(validateReminder({
      ...reminder,
      mode: 'clock',
      repeat: 'days',
      days: ['mon', 'mon'],
    }).days).toBe('Choose each weekday at most once.')
    expect(validateReminder({
      ...reminder,
      duration: { hours: '168', minutes: '0', seconds: '1' },
    }).duration).toBeTruthy()
  })

  it('counts label characters as the device does and refuses control characters', () => {
    expect(validateReminder({ ...reminder, label: '' }).label).toBe('Required.')
    expect(validateReminder({
      ...reminder,
      label: '😀'.repeat(500),
    })).toEqual({})
    expect(validateReminder({
      ...reminder,
      label: '😀'.repeat(501),
    }).label).toBe('At most 500 characters.')
    expect(validateReminder({ ...reminder, label: 'bad\tlabel' }).label).toBe('Use printable text without control characters.')
  })
})
