import type { TraceView } from '@/api/gen'
import { describe, expect, it } from 'vitest'
import { at, span } from './fixtures'
import { rootIdsOf, upsertSpan, viewOf, withFeedSpans } from './trace-view'

describe('trace view', () => {
  it('finds the roots: no parent, or one the trace lacks', () => {
    expect(rootIdsOf([
      span({
        spanId: 'b',
        parentId: 'remote',
        start: at(5),
      }),
      span({
        spanId: 'a',
        start: at(0),
      }),
      span({
        spanId: 'c',
        parentId: 'a',
        start: at(1),
      }),
    ])).toEqual(['a', 'b'])
  })

  it('folds a span in: a device root makes a server root its child, and the summary follows', () => {
    let view: TraceView = viewOf(span({
      spanId: 'srv',
      parentId: 'dev',
      start: at(10),
      durationMs: 20,
      live: true,
      seq: 2,
    }))
    expect(view.rootIds).toEqual(['srv'])
    view = upsertSpan(view, span({
      spanId: 'dev',
      name: 'voice.turn',
      service: 'cadence-device',
      start: at(0),
      durationMs: 100,
      seq: 3,
    }))
    expect(view).toMatchObject({
      rootIds: ['dev'],
      name: 'voice.turn',
      live: true,
      durationMs: 100,
      services: ['cadence-device', 'cadence-server'],
      seq: 3,
    })
    expect(view.spans!.map(s => s.spanId)).toEqual(['dev', 'srv'])
    view = upsertSpan(view, span({
      spanId: 'srv',
      parentId: 'dev',
      start: at(10),
      durationMs: 30,
      status: 'error',
      seq: 4,
    }))
    expect(view).toMatchObject({ live: false, error: true })
  })

  it('keeps the view when a span is older than its copy or of another trace', () => {
    const view = viewOf(span({ spanId: 'a', seq: 5 }))
    expect(upsertSpan(view, span({ spanId: 'a', seq: 4 }))).toBe(view)
    expect(upsertSpan(view, span({ spanId: 'x', traceId: 'other', seq: 9 }))).toBe(view)
  })
})

describe('a read trace with the feed\'s spans', () => {
  it('folds the feed\'s newer spans over the read and keeps the read\'s newer ones', () => {
    const read = upsertSpan(viewOf(span({ spanId: 'a', live: true, seq: 5 })), span({ spanId: 'b', parentId: 'a', seq: 6 }))
    const view = withFeedSpans(read, [
      span({ spanId: 'a', durationMs: 90, seq: 7 }),
      span({
        spanId: 'b',
        parentId: 'a',
        durationMs: 1,
        seq: 4,
      }),
    ])!
    expect(view.live).toBe(false)
    expect(view.spans!.find(s => s.spanId === 'b')!.seq).toBe(6)
  })

  it('shows the feed\'s spans alone before a read has the trace', () => {
    expect(withFeedSpans(undefined, [])).toBeUndefined()
    expect(withFeedSpans(undefined, [
      span({ spanId: 'a' }),
      span({ spanId: 'b', parentId: 'a' }),
    ])!.rootIds).toEqual(['a'])
  })
})
