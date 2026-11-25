import type { MetricsOutBody } from '@/api/gen'
import { describe, expect, it } from 'vitest'
import { downsample, drawRuns, lineSegments, seriesByAttr, seriesOf, usedShare } from './series'

const T0 = Date.parse('2026-11-12T15:00:00Z')

function at(seconds: number): string {
  return new Date(T0 + seconds * 1000).toISOString()
}

const BODY: MetricsOutBody = {
  truncated: false,
  series: [
    {
      name: 'system.cpu.utilization',
      kind: 'gauge',
      attrs: { cpu: '0' },
      points: [
        {
          ts: at(0),
          value: 0.9,
        },
      ],
    },
    {
      name: 'system.cpu.utilization',
      kind: 'gauge',
      attrs: {},
      points: [
        {
          ts: at(0),
          value: 0.25,
        },
        {
          ts: at(10),
          value: 0.5,
        },
      ],
    },
    {
      name: 'system.filesystem.free',
      kind: 'gauge',
      attrs: { mountpoint: '/data' },
      points: [
        {
          ts: at(0),
          value: 100,
        },
      ],
    },
    {
      name: 'system.filesystem.free',
      kind: 'gauge',
      attrs: { mountpoint: '/tmp' },
      points: null,
    },
  ],
}

describe('a metric\'s series', () => {
  it('is the one whose attributes are exactly those asked for: the total, not a core', () => {
    expect(seriesOf(BODY, 'system.cpu.utilization')).toEqual([
      { at: T0, value: 0.25 },
      { at: T0 + 10_000, value: 0.5 },
    ])
    expect(seriesOf(BODY, 'system.cpu.utilization', { cpu: '0' })).toEqual([
      { at: T0, value: 0.9 },
    ])
    expect(seriesOf(BODY, 'system.temperature')).toEqual([])
    expect(seriesOf(undefined, 'system.cpu.utilization')).toEqual([])
  })

  it('splits by one attribute, each value its own series', () => {
    expect(seriesByAttr(BODY, 'system.filesystem.free', 'mountpoint')).toEqual(new Map([
      [
        '/data',
        [
          { at: T0, value: 100 },
        ],
      ],
      ['/tmp', []],
    ]))
  })

  it('skips a point it cannot read', () => {
    const odd = {
      truncated: false,
      series: [
        {
          name: 'x',
          kind: 'gauge',
          attrs: null,
          points: [
            { ts: 'soon', value: 1 },
            {
              ts: at(0),
              value: Number.NaN,
            },
            {
              ts: at(1),
              value: 2,
            },
          ],
        },
      ],
    } as MetricsOutBody
    expect(seriesOf(odd, 'x')).toEqual([
      { at: T0 + 1000, value: 2 },
    ])
  })
})

describe('drawing a series', () => {
  it('averages runs of points down to the most a chart draws', () => {
    const samples = Array.from({ length: 10 }, (_, i) => ({ at: T0 + i * 1000, value: i }))
    expect(downsample(samples, 5)).toEqual([
      { at: T0 + 500, value: 0.5 },
      { at: T0 + 2500, value: 2.5 },
      { at: T0 + 4500, value: 4.5 },
      { at: T0 + 6500, value: 6.5 },
      { at: T0 + 8500, value: 8.5 },
    ])
    expect(downsample(samples, 20)).toBe(samples)
  })

  it('breaks the line where the device sent nothing for over three of its usual steps', () => {
    const samples = [0, 10, 20, 30, 100, 110, 120].map(s => ({ at: T0 + s * 1000, value: s }))
    expect(lineSegments(samples).map(run => run.map(s => s.value))).toEqual([
      [0, 10, 20, 30],
      [100, 110, 120],
    ])
    expect(lineSegments([])).toEqual([])
  })
})

describe('the runs a chart draws', () => {
  it('break at a gap first, then thin each run, so a short gap stays a gap however long the window', () => {
    const before = Array.from({ length: 500 }, (_, i) => ({ at: T0 + i * 10_000, value: 1 }))
    // a minute without points: shorter than a run of points averaged into one, longer than three steps
    const after = Array.from({ length: 500 }, (_, i) => ({ at: T0 + 5_060_000 + i * 10_000, value: 2 }))
    const runs = drawRuns([
      ...before,
      ...after,
    ], 50)
    expect(runs).toHaveLength(2)
    expect(runs.every(run => run.length <= 25)).toBe(true)
    expect(runs[0]!.every(s => s.value === 1)).toBe(true)
    expect(runs[1]!.every(s => s.value === 2)).toBe(true)
  })

  it('keep a short series as it is', () => {
    const samples = [0, 10, 20].map(s => ({ at: T0 + s * 1000, value: s }))
    expect(drawRuns(samples, 50)).toEqual([samples])
  })
})

describe('a mount\'s used share', () => {
  it('is 1 - free / limit at each round, its limit from the same round or the one before', () => {
    const free = [
      { at: T0, value: 300 },
      { at: T0 + 10_000, value: 200 },
      { at: T0 + 20_000, value: 100 },
    ]
    const limit = [
      { at: T0, value: 400 },
      { at: T0 + 20_000, value: 400 },
    ]
    expect(usedShare(free, limit)).toEqual([
      { at: T0, value: 0.25 },
      { at: T0 + 10_000, value: 0.5 },
      { at: T0 + 20_000, value: 0.75 },
    ])
  })

  it('is nothing where no positive limit came before', () => {
    expect(usedShare([
      { at: T0, value: 1 },
    ], [])).toEqual([])
    expect(usedShare([
      { at: T0, value: 1 },
    ], [
      { at: T0, value: 0 },
    ])).toEqual([])
  })
})
