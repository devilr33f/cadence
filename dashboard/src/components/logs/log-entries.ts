import type { LogView } from '@/api/gen'
import type { LogEntry, LogRecord } from '@/components/data/log-view'
import type { StreamEvent } from '@/lib/live/store'
import type { LogFilter } from '@/lib/logs'
import { count, text } from '@/lib/live/data'
import { lineKey } from '@/lib/log-paging'
import { matchesLine, severityOf } from '@/lib/logs'

function flags(line: { truncated?: boolean, continued?: boolean }): LogRecord['attrs'] {
  return line.truncated || line.continued
    ? {
        ...(line.continued && { continued: 'true' }),
        ...(line.truncated && { truncated: 'true' }),
      }
    : undefined
}

// stored lines as the view shows them, each its own id (two alike told apart by their count)
export function storedEntries(lines: readonly LogView[], names: ReadonlyMap<string, string>): LogRecord[] {
  const seen = new Map<string, number>()
  return lines.map((line) => {
    const key = lineKey(line)
    const n = seen.get(key) ?? 0
    seen.set(key, n + 1)
    return {
      id: `${key}#${n}`,
      at: line.ts,
      severity: severityOf(line.severity),
      source: line.source,
      // a deleted device's lines keep its id alone
      device: line.deviceId === undefined
        ? undefined
        : (line.deviceName ?? names.get(line.deviceId) ?? line.deviceId),
      message: line.body,
      attrs: flags(line),
    }
  })
}

// a live line's fields, read for what they are
function liveFields(event: StreamEvent) {
  return {
    deviceId: text(event.data, 'deviceId') ?? event.deviceId,
    source: text(event.data, 'source') ?? '',
    severity: count(event.data, 'severity') ?? 0,
    body: text(event.data, 'body') ?? '',
    ts: text(event.data, 'ts') ?? event.at,
  }
}

const GAPS: Record<string, string> = {
  reconnect: 'Lines may be missing here: the live feed reconnected.',
  dropped: 'Lines may be missing here: this tab fell behind the live feed.',
}

// the tail's entries, oldest first: the lines since it started that match the filter, and its marks
export function liveEntries(events: readonly StreamEvent[], since: number, filter: LogFilter, names: ReadonlyMap<string, string>): LogEntry[] {
  const out: LogEntry[] = []
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i]!
    if (event.seq < since)
      continue
    const id = `live-${event.seq}`
    if (event.type === 'log_line_dropped') {
      out.push({ kind: 'mark', id, text: `${(count(event.data, 'count') ?? 0).toLocaleString('en-GB')} lines were not sent: the live feed sends at most 50 a second.` })
      continue
    }
    if (event.type === 'log_gap') {
      out.push({ kind: 'mark', id, text: GAPS[text(event.data, 'cause') ?? ''] ?? GAPS.dropped! })
      continue
    }
    const line = liveFields(event)
    if (!matchesLine(line, filter))
      continue
    out.push({
      id,
      at: line.ts,
      severity: severityOf(line.severity),
      source: line.source,
      device: line.deviceId === undefined
        ? undefined
        : (names.get(line.deviceId) ?? line.deviceId),
      message: line.body,
    })
  }
  return out
}

// how many lines matching the filter came after `newest` (the last one a paused tail shows)
export function linesAfter(events: readonly StreamEvent[], newest: number, filter: LogFilter): number {
  let n = 0
  for (const event of events) {
    if (event.seq <= newest)
      break
    if (event.type === 'log_line' && matchesLine(liveFields(event), filter))
      n++
  }
  return n
}
