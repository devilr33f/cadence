import type { LogView } from '@/api/gen'

// where a page of stored lines starts: before `until` (exclusive), leaving out the lines at `boundary` that an earlier
// page already showed (`seen`, a key per line, a line twice for two of the same)
export interface LogPageParam {
  until?: string
  boundary?: string
  seen?: readonly string[]
}

export interface LogPage {
  lines: readonly LogView[]
  // how many lines the server sent, the seen ones counted: a page as long as the limit may have more behind it
  raw: number
  param?: LogPageParam
}

const RFC3339 = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/
const MICROS = 1_000_000

// a microsecond after a stored line's time, which the store keeps to the microsecond: `until` there still reads the
// lines that share that time, since the server orders lines by time alone and a page may end inside a run of them
export function justAfter(ts: string): string | undefined {
  const match = RFC3339.exec(ts)
  if (!match)
    return undefined
  let seconds = Date.parse(`${match[1]}${match[3]}`)
  if (Number.isNaN(seconds))
    return undefined
  let micros = Number((match[2] ?? '').padEnd(6, '0').slice(0, 6)) + 1
  if (micros >= MICROS) {
    seconds += 1000
    micros -= MICROS
  }
  return `${new Date(seconds).toISOString().slice(0, 19)}.${String(micros).padStart(6, '0')}Z`
}

// a line as a whole: two lines alike in every field are told apart by count only
export function lineKey(line: LogView): string {
  return JSON.stringify([
    line.ts,
    line.observedTs,
    line.deviceId ?? '',
    line.source,
    line.severity,
    line.body,
    line.truncated,
    line.continued,
  ])
}

// what the page after the last loaded one asks for; nothing once a page came back short
export function nextLogPage(pages: readonly LogPage[], limit: number): LogPageParam | undefined {
  const last = pages.at(-1)
  if (!last || last.raw < limit)
    return undefined
  // a whole page of lines at one time, each shown already: the rest of that time is skipped, not asked for again
  if (last.lines.length === 0) {
    return last.param?.boundary === undefined
      ? undefined
      : { until: last.param.boundary }
  }
  const oldest = pages.flatMap(page => page.lines).at(-1)!
  const until = justAfter(oldest.ts)
  if (until === undefined)
    return { until: oldest.ts }
  const seen = pages.flatMap(page => page.lines).filter(line => line.ts === oldest.ts).map(lineKey)
  return { until, boundary: oldest.ts, seen }
}

// a page's lines less those at the boundary an earlier page showed, each once
export function unseenLines(lines: readonly LogView[], param: LogPageParam): LogView[] {
  if (param.boundary === undefined || !param.seen?.length)
    return [...lines]
  const left = new Map<string, number>()
  for (const key of param.seen)
    left.set(key, (left.get(key) ?? 0) + 1)
  return lines.filter((line) => {
    if (line.ts !== param.boundary)
      return true
    const key = lineKey(line)
    const count = left.get(key) ?? 0
    if (count === 0)
      return true
    left.set(key, count - 1)
    return false
  })
}
