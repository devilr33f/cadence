import { describe, expect, it } from 'vitest'
import { failureFacts, traceFailure } from './failure'
import { at, span } from './fixtures'

describe('trace failure', () => {
  it('picks the deepest failed span and says why, in codes', () => {
    const spans = [
      span({
        spanId: 'root',
        name: 'voice.session',
        status: 'error',
        statusCode: 'stt_unavailable',
        attrs: { 'cadence.failed_stage': 'stt' },
      }),
      span({
        spanId: 'stt',
        name: 'stt.stream',
        parentId: 'root',
        status: 'error',
        statusCode: 'stt_unavailable',
        start: at(1),
      }),
      span({
        spanId: 'sock',
        name: 'uniproxy.stt',
        parentId: 'stt',
        status: 'error',
        statusCode: 'closed_1011',
        start: at(2),
        attrs: {
          'error.type': 'closed_1011',
          'cadence.outcome': 'closed',
          'websocket.close_code': 1011,
          'error.go_type': ['websocket.CloseError'],
        },
      }),
      span({
        spanId: 'hs',
        name: 'GET uniproxy.alice.yandex.net',
        parentId: 'sock',
        status: 'ok',
        start: at(2),
      }),
    ]
    const f = traceFailure(spans)
    expect(f?.span.spanId).toBe('sock')
    expect(f?.path).toEqual(['voice.session', 'stt.stream'])
    expect(f?.facts).toEqual([
      { label: 'Code', value: 'closed_1011' },
      { label: 'Outcome', value: 'closed' },
      { label: 'Close code', value: '1011' },
      { label: 'Go type', value: 'websocket.CloseError' },
    ])
  })

  it('takes the earliest of equally deep failures, and none when nothing failed', () => {
    const spans = [
      span({ spanId: 'r' }),
      span({
        spanId: 'late',
        parentId: 'r',
        status: 'error',
        statusCode: 'timeout',
        start: at(9),
      }),
      span({
        spanId: 'early',
        parentId: 'r',
        status: 'error',
        statusCode: 'tts_failed',
        start: at(3),
      }),
    ]
    expect(traceFailure(spans)?.span.spanId).toBe('early')
    expect(traceFailure([
      span({ spanId: 'ok', status: 'ok' }),
    ])).toBeUndefined()
  })

  it('has no facts for a span that did not fail', () => {
    expect(failureFacts(span({
      spanId: 'a',
      status: 'ok',
      attrs: { 'error.type': 'x' },
    }))).toEqual([])
  })
})
