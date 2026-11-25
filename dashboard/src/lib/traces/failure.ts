import type { SpanView } from '@/api/gen'
import { attrText } from './format'

export interface FailureFact {
  label: string
  value: string
}

// the attributes that say why a span failed, in the order a reader wants them: all codes and numbers
const FAILURE_ATTRS: readonly (readonly [key: string, label: string])[] = [
  ['error.type', 'Error type'],
  ['cadence.outcome', 'Outcome'],
  ['cadence.detail', 'Detail'],
  ['cadence.failed_stage', 'Failed stage'],
  ['cadence.failed_detail', 'Cause'],
  ['websocket.close_code', 'Close code'],
  ['websocket.close_reason', 'Close reason'],
  ['cadence.directive', 'Directive'],
  ['cadence.socket_error', 'Socket error'],
  ['http.response.status_code', 'HTTP status'],
  ['error.go_type', 'Go type'],
]

function isFailed(span: SpanView): boolean {
  return span.status === 'error'
}

// a span's failure facts: its code, then each failure attribute it has; none for a span that did not fail
export function failureFacts(span: SpanView): FailureFact[] {
  if (!isFailed(span))
    return []
  const attrs = span.attrs ?? {}
  const facts: FailureFact[] = []
  if (span.statusCode)
    facts.push({ label: 'Code', value: span.statusCode })
  for (const [key, label] of FAILURE_ATTRS) {
    const value = attrs[key]
    if (value === undefined || value === null || value === '')
      continue
    const text = Array.isArray(value)
      ? value.map(attrText).join(' > ')
      : attrText(value)
    // error.type repeats the code most of the time
    if (key === 'error.type' && text === span.statusCode)
      continue
    facts.push({ label, value: text })
  }
  return facts
}

export interface TraceFailure {
  // the deepest failed span: the one nearest the cause
  span: SpanView
  facts: FailureFact[]
  // the failed spans above it, root first, for the path to it
  path: string[]
}

// the trace's failure as one line: its deepest failed span (the earliest started among equals) and why
export function traceFailure(spans: readonly SpanView[]): TraceFailure | undefined {
  const byId = new Map(spans.map(s => [s.spanId, s]))
  const depth = (s: SpanView): number => {
    let d = 0
    const seen = new Set<string>()
    for (let p = s.parentId; p && byId.has(p) && !seen.has(p); p = byId.get(p)!.parentId) {
      seen.add(p)
      d++
    }
    return d
  }
  let best: SpanView | undefined
  let bestDepth = -1
  for (const s of spans) {
    if (!isFailed(s))
      continue
    const d = depth(s)
    if (d > bestDepth || (d === bestDepth && best && Date.parse(s.start) < Date.parse(best.start))) {
      best = s
      bestDepth = d
    }
  }
  if (!best)
    return undefined
  const path: string[] = []
  const seen = new Set<string>()
  for (let p = best.parentId; p && byId.has(p) && !seen.has(p); p = byId.get(p)!.parentId) {
    seen.add(p)
    const parent = byId.get(p)!
    if (isFailed(parent))
      path.unshift(parent.name)
  }
  return {
    span: best,
    facts: failureFacts(best),
    path,
  }
}
