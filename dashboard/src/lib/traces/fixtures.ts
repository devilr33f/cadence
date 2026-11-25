import type { SpanView, TraceSummary } from '@/api/gen'

export const T0 = Date.parse('2026-11-16T10:00:00.000Z')

export function at(ms: number): string {
  return new Date(T0 + ms).toISOString()
}

// a finished server span of trace t1 unless told otherwise
export function span(patch: Partial<SpanView> & { spanId: string }): SpanView {
  return {
    traceId: 't1',
    name: patch.spanId,
    service: 'cadence-server',
    start: at(0),
    durationMs: 10,
    live: false,
    seq: 1,
    ...patch,
  }
}

export function summary(patch: Partial<TraceSummary> & { traceId: string }): TraceSummary {
  return {
    name: 'voice.turn',
    start: at(0),
    durationMs: 100,
    live: false,
    error: false,
    spans: 1,
    services: ['cadence-server'],
    seq: 1,
    ...patch,
  }
}
