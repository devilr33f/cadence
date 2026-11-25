import { describe, expect, it } from 'vitest'
import { at, span, T0 } from './fixtures'
import { axisTicks, layoutWaterfall, liveDuration } from './waterfall'

describe('waterfall', () => {
  it('nests spans depth first under their roots, children by start', () => {
    const spans = [
      span({
        spanId: 'root',
        start: at(0),
        durationMs: 100,
      }),
      span({
        spanId: 'late',
        parentId: 'root',
        start: at(60),
        durationMs: 40,
      }),
      span({
        spanId: 'early',
        parentId: 'root',
        start: at(10),
        durationMs: 20,
      }),
      span({
        spanId: 'grand',
        parentId: 'early',
        start: at(15),
        durationMs: 5,
      }),
      span({
        spanId: 'orphan',
        parentId: 'gone',
        start: at(5),
        durationMs: 5,
      }),
    ]
    const w = layoutWaterfall(spans, ['root', 'orphan'], T0)
    expect(w.rows.map(r => [r.span.spanId, r.depth])).toEqual([
      ['root', 0],
      ['early', 1],
      ['grand', 2],
      ['late', 1],
      ['orphan', 0],
    ])
    expect(w.totalMs).toBe(100)
    const late = w.rows.find(r => r.span.spanId === 'late')!
    expect(late.left).toBeCloseTo(0.6)
    expect(late.width).toBeCloseTo(0.4)
    expect(late.offsetMs).toBe(60)
  })

  it('places a span\'s events on the axis and from its own start', () => {
    const w = layoutWaterfall([
      span({
        spanId: 'r',
        start: at(0),
        durationMs: 200,
        events: [
          {
            name: 'wake_detected',
            at: at(50),
          },
          {
            name: 'first_sample_played',
            at: at(150),
            attrs: { ms: 3 },
          },
        ],
      }),
    ], ['r'], T0)
    expect(w.rows[0]!.marks).toEqual([
      { name: 'wake_detected', offsetMs: 50, at: 0.25 },
      {
        name: 'first_sample_played',
        offsetMs: 150,
        at: 0.75,
        attrs: { ms: 3 },
      },
    ])
  })

  it('grows a live span to now, and the axis with it', () => {
    const live = span({
      spanId: 'r',
      start: at(0),
      durationMs: 10,
      live: true,
    })
    expect(liveDuration(live, T0 + 500)).toBe(500)
    expect(liveDuration(live, T0 + 5)).toBe(10)
    const w = layoutWaterfall([
      live,
      span({
        spanId: 'c',
        parentId: 'r',
        start: at(100),
        durationMs: 100,
      }),
    ], ['r'], T0 + 400)
    expect(w.totalMs).toBe(400)
    expect(w.rows[0]!.width).toBe(1)
    expect(w.rows[1]!.width).toBeCloseTo(0.25)
  })

  it('places every span once, a cycle too', () => {
    const w = layoutWaterfall([
      span({
        spanId: 'a',
        parentId: 'b',
        start: at(0),
      }),
      span({
        spanId: 'b',
        parentId: 'a',
        start: at(1),
      }),
    ], [], T0)
    expect(w.rows.map(r => r.span.spanId)).toEqual(['a', 'b'])
  })

  it('ticks an axis at round steps', () => {
    expect(axisTicks(1000).map(t => t.ms)).toEqual([0, 200, 400, 600, 800, 1000])
    expect(axisTicks(730).map(t => t.ms)).toEqual([0, 200, 400, 600])
    expect(axisTicks(0)).toEqual([
      { ms: 0, at: 0 },
    ])
    expect(axisTicks(1000)[1]!.at).toBeCloseTo(0.2)
  })

  it('lays out nothing for no spans', () => {
    expect(layoutWaterfall([], [], T0).rows).toEqual([])
  })
})
