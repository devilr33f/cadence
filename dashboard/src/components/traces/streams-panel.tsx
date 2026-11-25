import type { Column } from '@/components/data/data-table'
import type { StreamRow } from '@/lib/traces/streams'
import { Link } from '@tanstack/react-router'
import { ArrowDownIcon, ArrowUpIcon } from 'lucide-react'
import { useId, useMemo } from 'react'
import { DataTable } from '@/components/data/data-table'
import { useNow } from '@/hooks/use-now'
import { rateText } from '@/lib/traces/format'
import { streamRow, streamTotals } from '@/lib/traces/streams'
import { bytesText } from '@/lib/units'
import { RunningDuration } from './running-duration'
import { ServiceTag } from './service-tag'

function Rate({ bytes, speed }: { bytes: number, speed: number }) {
  return (
    <span className="flex flex-col items-end leading-tight">
      <span data-numeric className="font-semibold">{rateText(speed)}</span>
      <span data-numeric className="text-xs text-muted-foreground">{bytesText(bytes)}</span>
    </span>
  )
}

function columns(names: ReadonlyMap<string, string>): Column<StreamRow>[] {
  return [
    {
      id: 'name',
      header: 'Span',
      cell: r => (
        <Link to="/traces/$traceId" params={{ traceId: r.span.traceId }} className="font-mono text-xs font-medium underline-offset-3 hover:underline">
          {r.span.name}
        </Link>
      ),
    },
    {
      id: 'where',
      header: 'Ran on',
      cell: r => (
        <span className="flex items-center gap-1.5">
          <ServiceTag service={r.span.service} />
          {r.span.deviceId && <span className="text-muted-foreground">{r.span.deviceName || names.get(r.span.deviceId) || ''}</span>}
        </span>
      ),
      className: 'max-md:hidden',
    },
    {
      id: 'in',
      header: 'In',
      align: 'end',
      cell: r => <Rate bytes={r.bytesIn} speed={r.speedIn} />,
    },
    {
      id: 'out',
      header: 'Out',
      align: 'end',
      cell: r => <Rate bytes={r.bytesOut} speed={r.speedOut} />,
    },
    {
      id: 'running',
      header: 'Running for',
      align: 'end',
      cell: r => <RunningDuration start={r.span.start} durationMs={r.span.durationMs} live />,
      className: 'max-sm:hidden',
    },
  ]
}

// the live spans that move bytes (a relay, an upload, a provider's stream): what moved and how fast now. a stream
// joins at its first counter update after the page opened, at most a second into it, and leaves at its end
export function StreamsPanel({ streams, names }: { streams: readonly StreamRow[], names: ReadonlyMap<string, string> }) {
  const titleId = useId()
  const now = useNow(streams.length > 0)
  // a quiet stream's speeds fall to zero between its updates
  const rows = useMemo(() => streams.map(r => streamRow(r.span, r.heardAt, now ?? r.heardAt)), [streams, now])
  const cols = useMemo(() => columns(names), [names])
  const total = streamTotals(rows)
  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-3">
      <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1">
        <h2 id={titleId} className="text-lg font-semibold">Streams</h2>
        {rows.length > 0 && (
          <p className="flex gap-4 text-sm text-muted-foreground">
            <span className="flex items-center gap-1">
              <ArrowDownIcon aria-hidden="true" className="size-3.5" />
              <span className="sr-only">In</span>
              <span data-numeric className="font-semibold text-foreground">{rateText(total.speedIn)}</span>
            </span>
            <span className="flex items-center gap-1">
              <ArrowUpIcon aria-hidden="true" className="size-3.5" />
              <span className="sr-only">Out</span>
              <span data-numeric className="font-semibold text-foreground">{rateText(total.speedOut)}</span>
            </span>
          </p>
        )}
      </div>
      {rows.length === 0
        ? <p className="text-sm text-muted-foreground">No span is moving bytes now. A stream shows here while it runs, with its bytes and rates.</p>
        : (
            <DataTable
              caption="Streams"
              columns={cols}
              rows={rows}
              rowKey={r => r.span.spanId}
              empty={null}
            />
          )}
    </section>
  )
}
