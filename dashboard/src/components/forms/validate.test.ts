import { describe, expect, it } from 'vitest'
import { ALARM_DAYS, ALARMS_CONFIG, OUTPUT_SCREEN_CONFIG, SEEK, SOLOIST_CONFIG, SYSTEM_METRICS_CONFIG, URL_PLAY } from '@/test/device-schemas'
import { buildForm } from './json-schema'
import { validateForm } from './validate'

function errors(schema: unknown, value: unknown): Record<string, string> {
  return Object.fromEntries(validateForm(buildForm(schema, value), value))
}

describe('client validation mirrors the schema', () => {
  it('passes a value that fits', () => {
    expect(errors(URL_PLAY, {
      url: 'https://radio.example/stream',
      mode: 'overlay',
      metadata: { title: 'News' },
      startMs: 0,
    })).toEqual({})
  })

  it('holds a screen\'s delay trim to its bounds and its volume mode to its enum', () => {
    expect(errors(OUTPUT_SCREEN_CONFIG, {
      delayMs: -250,
      volume: { mode: 'system' },
    })).toEqual({})
    expect(errors(OUTPUT_SCREEN_CONFIG, { delayMs: 1001 })).toEqual({ '/delayMs': 'At most 1000.' })
    expect(errors(OUTPUT_SCREEN_CONFIG, { delayMs: -1001 })).toEqual({ '/delayMs': 'At least -1000.' })
  })

  it('names a required field that is missing', () => {
    expect(errors(ALARMS_CONFIG, {})).toEqual({ '/timeZone': 'Required.' })
    expect(errors(SEEK, {})).toEqual({ '/positionMs': 'Required.' })
  })

  it('leaves the required fields of an optional part that is absent alone', () => {
    const schema = {
      type: 'object',
      properties: {
        quiet: {
          type: 'object',
          properties: {
            start: { type: 'string' },
          },
          required: ['start'],
        },
      },
    }
    expect(errors(schema, {})).toEqual({})
    expect(errors(schema, { quiet: {} })).toEqual({ '/quiet/start': 'Required.' })
  })

  it.each<[string, unknown, Record<string, string>]>([
    [
      'at the shortest',
      { timeZone: '' },
      { '/timeZone': 'At least 1 character.' },
    ],
    [
      'past the longest',
      {
        timeZone: 'x'.repeat(65),
      },
      { '/timeZone': 'At most 64 characters.' },
    ],
  ])('a string %s', (_, value, expected) => {
    expect(errors(ALARMS_CONFIG, value)).toEqual(expected)
  })

  it('counts characters, not code units', () => {
    expect(errors(ALARMS_CONFIG, {
      timeZone: '🕰️'.repeat(30),
    })).toEqual({})
  })

  it('checks a pattern', () => {
    expect(errors(SOLOIST_CONFIG, { deviceName: ' Kitchen' })).toEqual({
      '/deviceName': 'Must match ^[A-Za-z0-9._-]([A-Za-z0-9._ -]*[A-Za-z0-9._-])?$.',
    })
    expect(errors(SOLOIST_CONFIG, { deviceName: 'Kitchen speaker' })).toEqual({})
  })

  it.each<[unknown, Record<string, string>]>([
    [
      9,
      { '/intervalS': 'At least 10.' },
    ],
    [10, {}],
    [300, {}],
    [
      301,
      { '/intervalS': 'At most 300.' },
    ],
  ])('bounds an integer: %s', (intervalS, expected) => {
    expect(errors(SYSTEM_METRICS_CONFIG, { intervalS })).toEqual(expected)
  })

  it('bounds exclusively', () => {
    const schema = {
      type: 'object',
      properties: {
        gain: { type: 'number', exclusiveMinimum: 0, exclusiveMaximum: 1 },
      },
    }
    expect(errors(schema, { gain: 0 })).toEqual({ '/gain': 'More than 0.' })
    expect(errors(schema, { gain: 1 })).toEqual({ '/gain': 'Less than 1.' })
    expect(errors(schema, { gain: 0.5 })).toEqual({})
  })

  it('checks a list\'s length, its items once each and each item', () => {
    expect(errors(ALARM_DAYS, { days: [] })).toEqual({ '/days': 'At least 1 item.' })
    expect(errors(ALARM_DAYS, {
      days: ['mon', 'mon'],
    })).toEqual({ '/days': 'Each item only once.' })
    const tags = {
      type: 'object',
      properties: {
        names: {
          type: 'array',
          maxItems: 2,
          items: { type: 'string', maxLength: 3 },
        },
      },
    }
    expect(errors(tags, {
      names: ['a', 'b', 'c'],
    })).toEqual({ '/names': 'At most 2 items.' })
    expect(errors(tags, {
      names: ['abcd'],
    })).toEqual({ '/names': 'Item 1: At most 3 characters.' })
  })
})
