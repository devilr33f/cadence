import type { ReactNode } from 'react'
import type { Column } from '@/components/data/data-table'
import type { TraceRow } from '@/lib/traces/book'
import { Link } from '@tanstack/react-router'
import { useMemo } from 'react'
import { DataTable } from '@/components/data/data-table'
import { clockText } from '@/lib/format-time'
import { RunningDuration } from './running-duration'
import { ServiceTag } from './service-tag'
import { TraceStatus } from './trace-status'

const NEWEST = { column: 'start', direction: 'desc' } as const

function deviceCell(names: ReadonlyMap<string, string>, deviceId: string | undefined): ReactNode {
  if (!deviceId)
    return <span className="text-muted-foreground">—</span>
  return names.get(deviceId) ?? <span className="font-mono text-xs">{deviceId}</span>
}

function columns(names: ReadonlyMap<string, string>, timeZone: string | null): Column<TraceRow>[] {
  return [
    {
      id: 'start',
      header: 'Started',
      sortValue: r => r.start,
      firstSort: 'desc',
      cell: r => <time dateTime={r.startIso} className="font-semibold">{clockText(r.startIso, timeZone, true)}</time>,
    },
    {
      id: 'name',
      header: 'Trace',
      sortValue: r => r.name,
      cell: r => (
        <Link to="/traces/$traceId" params={{ traceId: r.traceId }} className="font-mono text-xs font-medium underline-offset-3 hover:underline">
          {r.name || r.traceId}
        </Link>
      ),
    },
    {
      id: 'device',
      header: 'Speaker',
      sortValue: r => r.deviceId
        ? (names.get(r.deviceId) ?? r.deviceId)
        : null,
      cell: r => deviceCell(names, r.deviceId),
      className: 'max-md:hidden',
    },
    {
      id: 'services',
      header: 'Ran on',
      cell: r => (
        <span className="flex gap-1">
          {r.services.map(s => <ServiceTag key={s} service={s} />)}
        </span>
      ),
      className: 'max-lg:hidden',
    },
    {
      id: 'duration',
      header: 'Duration',
      align: 'end',
      sortValue: r => r.durationMs,
      cell: r => <RunningDuration start={r.start} durationMs={r.durationMs} live={r.live} />,
    },
    {
      id: 'spans',
      header: 'Spans',
      align: 'end',
      sortValue: r => r.spans,
      cell: r => <span data-numeric>{r.spans}</span>,
      className: 'max-sm:hidden',
    },
    {
      id: 'status',
      header: 'Status',
      sortValue: r => r.live
        ? 0
        : r.error ? 1 : 2,
      cell: r => <TraceStatus live={r.live} error={r.error} />,
    },
  ]
}

// the traces newest first; a running one counts its time on and changes in place as its spans come
export function TraceTable({ rows, names, timeZone, status, empty, error }: {
  rows: readonly TraceRow[]
  names: ReadonlyMap<string, string>
  timeZone: string | null
  status: 'ready' | 'loading' | 'error'
  empty: ReactNode
  error: ReactNode
}) {
  const cols = useMemo(() => columns(names, timeZone), [names, timeZone])
  return (
    <DataTable
      caption="Traces"
      columns={cols}
      rows={rows}
      rowKey={r => r.traceId}
      initialSort={NEWEST}
      status={status}
      empty={empty}
      error={error}
      className="max-h-[70svh]"
    />
  )
}
