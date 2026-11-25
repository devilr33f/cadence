import { describe, expect, it } from 'vitest'
import { attrText, rateText, serviceLabel, spanDurationText } from './format'

describe('trace formats', () => {
  it('word a span\'s length at a profiler\'s precision', () => {
    expect([
      0,
      0.423,
      3.42,
      120.4,
      1240,
      59_999,
      123_000,
      3_725_000,
    ].map(spanDurationText)).toEqual([
      '0 ms',
      '0.42 ms',
      '3.4 ms',
      '120 ms',
      '1.24 s',
      '60 s',
      '2 m 03 s',
      '1 h 02 m',
    ])
  })

  it('word a rate in bytes a second, nothing moving as zero', () => {
    expect(rateText(0)).toBe('0 B/s')
    expect(rateText(-1)).toBe('0 B/s')
    expect(rateText(Number.NaN)).toBe('0 B/s')
    expect(rateText(1536)).toBe('1.5 KiB/s')
    expect(rateText(4.2 * 1024 * 1024)).toBe('4.2 MiB/s')
  })

  it('name the two services and keep any other', () => {
    expect(serviceLabel('cadence-server')).toBe('Server')
    expect(serviceLabel('cadence-device')).toBe('Device')
    expect(serviceLabel('other')).toBe('other')
  })

  it('set an attribute on one line', () => {
    expect(attrText('stt')).toBe('stt')
    expect(attrText(42)).toBe('42')
    expect(attrText(['a', 1])).toBe('["a",1]')
    expect(attrText(true)).toBe('true')
  })
})
