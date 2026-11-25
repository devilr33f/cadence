import { describe, expect, it } from 'vitest'
import { bytesText, percentText, temperatureText } from './units'

describe('units', () => {
  it('word bytes in binary steps, three figures at most', () => {
    expect([
      0,
      512,
      1024,
      12_700,
      431_000_000,
      2_080_000_000,
      1024 ** 4 * 3,
    ].map(bytesText)).toEqual([
      '0 B',
      '512 B',
      '1 KiB',
      '12.4 KiB',
      '411 MiB',
      '1.94 GiB',
      '3 TiB',
    ])
  })

  it('word a share in whole percents, tenths under ten', () => {
    expect([0, 0.004, 0.045, 0.1234, 1, 2.5].map(percentText)).toEqual([
      '0%',
      '0.4%',
      '4.5%',
      '12%',
      '100%',
      '250%',
    ])
  })

  it('word a temperature in tenths of a degree', () => {
    expect(temperatureText(47.25)).toBe('47.3 °C')
    expect(temperatureText(-3)).toBe('-3.0 °C')
  })
})
