import type { SpanView } from '@/api/gen'

export interface WaterfallMark {
  name: string
  // from the span's own start
  offsetMs: number
  // across the whole axis, 0 to 1
  at: number
  attrs?: Record<string, unknown>
}

export interface WaterfallRow {
  span: SpanView
  depth: number
  // the span's place on the axis, 0 to 1
  left: number
  width: number
  // the span's length as of now: a live one grows
  durationMs: number
  // from the trace's start
  offsetMs: number
  marks: WaterfallMark[]
}

export interface WaterfallTick {
  ms: number
  at: number
}

export interface Waterfall {
  rows: WaterfallRow[]
  startMs: number
  totalMs: number
  ticks: WaterfallTick[]
}

// a span's length now: a live one has run until now, and never less than the server last said
export function liveDuration(span: SpanView, now: number): number {
  if (!span.live)
    return Math.max(0, span.durationMs)
  return Math.max(span.durationMs, now - Date.parse(span.start))
}

// round steps for an axis of total ms: 1, 2 or 5 times a power of ten, about `count` of them
export function axisTicks(totalMs: number, count = 5): WaterfallTick[] {
  if (!(totalMs > 0)) {
    return [
      { ms: 0, at: 0 },
    ]
  }
  const raw = totalMs / count
  const power = 10 ** Math.floor(Math.log10(raw))
  const step = [1, 2, 5, 10].map(m => m * power).find(s => s >= raw) ?? 10 * power
  const ticks: WaterfallTick[] = []
  for (let ms = 0; ms <= totalMs + step * 1e-9; ms += step) {
    ticks.push({
      ms: Number(ms.toPrecision(12)),
      at: Math.min(1, ms / totalMs),
    })
  }
  return ticks
}

// the trace as rows: depth first from its roots, children by start, each placed on one time axis from the earliest
// start to the latest end (a live span reaching now). a span a cycle or a lost parent leaves out joins as a root
export function layoutWaterfall(spans: readonly SpanView[], rootIds: readonly string[], now: number): Waterfall {
  if (spans.length === 0) {
    return {
      rows: [],
      startMs: 0,
      totalMs: 0,
      ticks: axisTicks(0),
    }
  }
  const byId = new Map(spans.map(s => [s.spanId, s]))
  const children = new Map<string, SpanView[]>()
  for (const span of spans) {
    if (span.parentId && byId.has(span.parentId) && span.parentId !== span.spanId) {
      const list = children.get(span.parentId) ?? []
      list.push(span)
      children.set(span.parentId, list)
    }
  }
  const byStart = (a: SpanView, b: SpanView) => Date.parse(a.start) - Date.parse(b.start)
  for (const list of children.values())
    list.sort(byStart)

  const startMs = Math.min(...spans.map(s => Date.parse(s.start)))
  const endMs = Math.max(...spans.map(s => Date.parse(s.start) + liveDuration(s, now)))
  const totalMs = Math.max(0, endMs - startMs)
  const place = (ms: number) => totalMs > 0
    ? Math.min(1, Math.max(0, (ms - startMs) / totalMs))
    : 0

  const rows: WaterfallRow[] = []
  const placed = new Set<string>()
  const visit = (span: SpanView, depth: number) => {
    if (placed.has(span.spanId))
      return
    placed.add(span.spanId)
    const start = Date.parse(span.start)
    const durationMs = liveDuration(span, now)
    const left = place(start)
    rows.push({
      span,
      depth,
      left,
      width: Math.max(0, place(start + durationMs) - left),
      durationMs,
      offsetMs: start - startMs,
      marks: (span.events ?? []).map((event) => {
        const at = Date.parse(event.at)
        return {
          name: event.name,
          offsetMs: at - start,
          at: place(at),
          ...(event.attrs && Object.keys(event.attrs).length > 0 && { attrs: event.attrs }),
        }
      }),
    })
    for (const child of children.get(span.spanId) ?? [])
      visit(child, depth + 1)
  }
  const roots = rootIds.map(id => byId.get(id)).filter((s): s is SpanView => s !== undefined)
  for (const root of roots)
    visit(root, 0)
  for (const span of [...spans].sort(byStart))
    visit(span, 0)
  return {
    rows,
    startMs,
    totalMs,
    ticks: axisTicks(totalMs),
  }
}
