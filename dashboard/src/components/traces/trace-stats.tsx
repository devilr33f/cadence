import type { ReactNode } from 'react'
import type { TraceStats } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { describeError } from '@/api/describe-error'
import { useApi } from '@/api/use-api'
import { LoadingLines } from '@/components/data/states'
import { StatusBadge } from '@/components/data/status-badge'
import { STATS_EVERY_MS, traceStatsQueryOptions } from '@/lib/traces/queries'
import { bytesText } from '@/lib/units'

const COUNT = new Intl.NumberFormat('en')

function Figure({ label, children }: { label: string, children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd className="text-sm font-semibold tabular-nums">{children}</dd>
    </div>
  )
}

function exportText(stats: TraceStats): string {
  const e = stats.export
  if (!e.on)
    return 'Off'
  const parts = [`${COUNT.format(e.sent)} sent`]
  if (e.queued > 0)
    parts.push(`${COUNT.format(e.queued)} queued`)
  if (e.dropped > 0)
    parts.push(`${COUNT.format(e.dropped)} dropped`)
  if (e.failures > 0)
    parts.push(`${COUNT.format(e.failures)} failed`)
  return parts.join(' · ')
}

// what the server's trace store holds and has sent, read again every 2 s while the page shows (no event carries it)
export function TraceStatsBar() {
  const api = useApi()
  const stats = useQuery({
    ...traceStatsQueryOptions(api),
    refetchInterval: STATS_EVERY_MS,
    staleTime: 0,
  })
  if (stats.isPending)
    return <LoadingLines lines={2} label="Loading the trace store's figures" />
  if (stats.isError)
    return <p role="alert" className="text-sm text-destructive">{`The store's figures could not be read: ${describeError(stats.error)}`}</p>
  const s = stats.data
  const dropped = s.dropped + s.overflow
  return (
    <div className="flex flex-col gap-2">
      {!s.enabled && (
        <p className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          <StatusBadge tone="neutral" label="Tracing is off" />
          <span>
            Turn it on in
            {' '}
            <Link to="/settings" className="text-foreground underline underline-offset-4">Settings</Link>
            ; until then no span is recorded.
          </span>
        </p>
      )}
      <dl aria-label="The trace store" className="flex flex-wrap gap-x-8 gap-y-3 rounded-lg border bg-card px-4 py-3">
        <Figure label="Live spans">{COUNT.format(s.liveSpans)}</Figure>
        <Figure label="Traces kept">{`${COUNT.format(s.traces)} of ${COUNT.format(s.ringSize)}`}</Figure>
        <Figure label="Memory">{`${bytesText(s.bytes)} of ${bytesText(s.budget)}`}</Figure>
        <Figure label="Dropped">
          <span className={dropped > 0
            ? 'text-warning'
            : undefined}
          >
            {COUNT.format(dropped)}
          </span>
        </Figure>
        <Figure label="Bytes in">{bytesText(s.bytesIn)}</Figure>
        <Figure label="Bytes out">{bytesText(s.bytesOut)}</Figure>
        <Figure label="From devices">
          {`${COUNT.format(s.ingested)}${s.rejected > 0
            ? ` · ${COUNT.format(s.rejected)} refused`
            : ''}`}
        </Figure>
        <Figure label="Sampled">{`${Math.round(s.sampleRatio * 100)}%`}</Figure>
        <Figure label="Export">
          <span className={s.export.failures > 0 || s.export.dropped > 0
            ? 'text-warning'
            : undefined}
          >
            {exportText(s)}
          </span>
        </Figure>
      </dl>
    </div>
  )
}
