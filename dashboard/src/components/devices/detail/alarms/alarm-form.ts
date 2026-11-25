// the device's own argument rules (device/internal/alarms): the dashboard sends no session and no level, so a
// reminder stays one that never changes the volume (rule 7, owner 2026-11-16)

export const WEEKDAYS = [
  { id: 'mon', label: 'Monday', short: 'Mon' },
  { id: 'tue', label: 'Tuesday', short: 'Tue' },
  { id: 'wed', label: 'Wednesday', short: 'Wed' },
  { id: 'thu', label: 'Thursday', short: 'Thu' },
  { id: 'fri', label: 'Friday', short: 'Fri' },
  { id: 'sat', label: 'Saturday', short: 'Sat' },
  { id: 'sun', label: 'Sunday', short: 'Sun' },
] as const

export const MAX_TIMER_SECONDS = 86_400
export const MAX_REMINDER_SECONDS = 604_800
// the server speaks a reminder's text when it rings and makes its 80-rune label from it (home assistant's shape)
export const MAX_TEXT_RUNES = 500

const ID = /^[a-z0-9]{1,16}$/
const CLOCK = /^(?:[01]\d|2[0-3]):[0-5]\d$/
const DATE = /^(\d{4})-(\d{2})-(\d{2})$/

export interface DurationValues {
  hours: string
  minutes: string
  seconds: string
}

export interface TimerValues {
  id: string
  duration: DurationValues
}

export interface ReminderValues {
  id: string
  label: string
  mode: 'after' | 'clock'
  duration: DurationValues
  time: string
  repeat: 'once' | 'date' | 'days'
  date: string
  days: string[]
}

export type TimerErrors = Partial<Record<'id' | 'duration' | 'form', string>>
export type ReminderErrors = Partial<Record<'id' | 'label' | 'duration' | 'time' | 'date' | 'days' | 'form', string>>

function wholePart(value: string): number | undefined {
  if (value === '')
    return 0
  if (!/^\d+$/.test(value))
    return undefined
  const parsed = Number(value)
  return Number.isSafeInteger(parsed)
    ? parsed
    : undefined
}

// empty duration fields count as zero; anything typed must be a whole, non-negative, safely summed number
export function durationSeconds(values: DurationValues): number | undefined {
  const hours = wholePart(values.hours)
  const minutes = wholePart(values.minutes)
  const seconds = wholePart(values.seconds)
  if (hours === undefined || minutes === undefined || seconds === undefined)
    return undefined
  const total = hours * 3600 + minutes * 60 + seconds
  return Number.isSafeInteger(total)
    ? total
    : undefined
}

function idError(id: string): string | undefined {
  return ID.test(id)
    ? undefined
    : 'Use 1–16 lowercase letters or digits, with no spaces.'
}

function durationError(values: DurationValues, maximum: number, range: string): string | undefined {
  const seconds = durationSeconds(values)
  if (seconds === undefined)
    return 'Use whole, non-negative numbers of hours, minutes and seconds.'
  return seconds >= 1 && seconds <= maximum
    ? undefined
    : range
}

export function validateTimer(values: TimerValues): TimerErrors {
  const id = idError(values.id)
  const duration = durationError(values.duration, MAX_TIMER_SECONDS, 'A timer lasts 1 second to 24 hours.')
  return {
    ...(id && { id }),
    ...(duration && { duration }),
  }
}

function validDate(value: string): boolean {
  const match = DATE.exec(value)
  if (!match)
    return false
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  if (month < 1 || month > 12 || day < 1)
    return false
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const lengths = [
    31,
    leap ? 29 : 28,
    31,
    30,
    31,
    30,
    31,
    31,
    30,
    31,
    30,
    31,
  ]
  return day <= lengths[month - 1]!
}

function hasControlCharacter(value: string): boolean {
  for (const character of value) {
    const point = character.codePointAt(0)!
    if (point <= 0x1F || (point >= 0x7F && point <= 0x9F))
      return true
  }
  return false
}

function labelError(label: string): string | undefined {
  const length = [...label].length
  if (length < 1)
    return 'Required.'
  if (length > MAX_TEXT_RUNES)
    return `At most ${MAX_TEXT_RUNES} characters.`
  if (hasControlCharacter(label))
    return 'Use printable text without control characters.'
  return undefined
}

export function validateReminder(values: ReminderValues): ReminderErrors {
  const errors: ReminderErrors = {}
  const id = idError(values.id)
  const label = labelError(values.label)
  if (id)
    errors.id = id
  if (label)
    errors.label = label
  if (values.mode === 'after') {
    const duration = durationError(values.duration, MAX_REMINDER_SECONDS, 'A delayed reminder is 1 second to 7 days from now.')
    if (duration)
      errors.duration = duration
  }
  else {
    if (!CLOCK.test(values.time))
      errors.time = 'Use a time from 00:00 to 23:59.'
    if (values.repeat === 'date' && !validDate(values.date))
      errors.date = 'Use a real calendar date.'
    if (values.repeat === 'days') {
      const valid = values.days.every((day, index) =>
        WEEKDAYS.some(weekday => weekday.id === day) && values.days.indexOf(day) === index)
      if (values.days.length === 0)
        errors.days = 'Choose at least one day.'
      else if (!valid)
        errors.days = 'Choose each weekday at most once.'
    }
  }
  return errors
}

export function timerArgs(values: TimerValues): { id: string, seconds: number } {
  return {
    id: values.id,
    seconds: durationSeconds(values.duration)!,
  }
}

export type ReminderArgs = { id: string, text: string, inSeconds: number } | {
  id: string
  text: string
  time: string
  date?: string
  days?: string[]
}

export function reminderArgs(values: ReminderValues): ReminderArgs {
  if (values.mode === 'after') {
    return {
      id: values.id,
      text: values.label,
      inSeconds: durationSeconds(values.duration)!,
    }
  }
  return {
    id: values.id,
    text: values.label,
    time: values.time,
    ...(values.repeat === 'date' && { date: values.date }),
    ...(values.repeat === 'days' && {
      days: WEEKDAYS.map(day => day.id).filter(day => values.days.includes(day)),
    }),
  }
}
