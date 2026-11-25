import type { TraceView } from '@/api/gen'
import { Link, useParams } from '@tanstack/react-router'
import { ChevronLeftIcon, WaypointsIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Stamp } from '@/components/data/stamp'
import { EmptyState, ErrorState, LoadingLines } from '@/components/data/states'
import { buttonVariants } from '@/components/ui/button-variants'
import { useDeviceNames } from '@/hooks/use-device-names'
import { useNow } from '@/hooks/use-now'
import { useTimeZone } from '@/lib/time-zone'
import { serviceLabel } from '@/lib/traces/format'
import { layoutWaterfall } from '@/lib/traces/waterfall'
import { FailureSummary } from './failure-summary'
import { RunningDuration } from './running-duration'
import { SpanPanel } from './span-panel'
import { TraceStatus } from './trace-status'
import { useTraceDetail } from './use-trace-detail'
import { WaterfallView } from './waterfall-view'

function BackLink() {
  return (
    <Link to="/traces" className="mb-3 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ChevronLeftIcon aria-hidden="true" className="size-4" />
      Traces
    </Link>
  )
}

function Header({ view }: { view: TraceView }) {
  const timeZone = useTimeZone()
  const names = useDeviceNames()
  const services = (view.services ?? []).map(serviceLabel).join(' and ')
  return (
    <div className="mb-6 flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-mono text-xl font-semibold break-all">{view.name || view.traceId}</h1>
        <TraceStatus live={view.live} error={view.error} appearance="pill" />
      </div>
      <dl className="flex flex-wrap items-center gap-x-5 gap-y-1 text-sm text-muted-foreground">
        <div className="flex gap-1.5">
          <dt>Started</dt>
          <dd className="text-foreground"><Stamp at={view.start} timeZone={timeZone} /></dd>
        </div>
        <div className="flex gap-1.5">
          <dt>Duration</dt>
          <dd className="font-semibold text-foreground"><RunningDuration start={view.start} durationMs={view.durationMs} live={view.live} /></dd>
        </div>
        <div className="flex gap-1.5">
          <dt>Spans</dt>
          <dd data-numeric className="text-foreground">{(view.spans ?? []).length}</dd>
        </div>
        {services && (
          <div className="flex gap-1.5">
            <dt>Ran on</dt>
            <dd className="text-foreground">{services}</dd>
          </div>
        )}
        {view.deviceId && (
          <div className="flex gap-1.5">
            <dt>Speaker</dt>
            <dd>
              <Link to="/devices/$id" params={{ id: view.deviceId }} className="text-foreground underline-offset-4 hover:underline">
                {names.get(view.deviceId) ?? view.deviceId}
              </Link>
            </dd>
          </div>
        )}
        <div className="flex min-w-0 gap-1.5">
          <dt>Id</dt>
          <dd className="font-mono text-xs/5 break-all text-foreground">{view.traceId}</dd>
        </div>
      </dl>
    </div>
  )
}

function TraceBody({ view }: { view: TraceView }) {
  const names = useDeviceNames()
  const now = useNow(view.live)
  const spans = useMemo(() => view.spans ?? [], [view.spans])
  const waterfall = useMemo(
    () => layoutWaterfall(spans, view.rootIds ?? [], now ?? Date.now()),
    [spans, view.rootIds, now],
  )
  const [chosen, setChosen] = useState<string | undefined>(undefined)
  const selected = spans.find(s => s.spanId === chosen) ?? spans.find(s => s.spanId === view.rootIds?.[0]) ?? spans[0]
  return (
    <>
      <FailureSummary spans={spans} onSelect={setChosen} />
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <WaterfallView waterfall={waterfall} selected={selected?.spanId} onSelect={setChosen} />
        {selected && (
          <SpanPanel
            span={selected}
            deviceName={selected.deviceId
              ? names.get(selected.deviceId)
              : undefined}
          />
        )}
      </div>
    </>
  )
}

export function TracePage() {
  const { traceId } = useParams({ from: '/_authed/traces/$traceId' })
  // one route serves every trace: a selected span never carries over to the next
  return <TraceDetailView key={traceId} id={traceId} />
}

function TraceDetailView({ id }: { id: string }) {
  const detail = useTraceDetail(id)
  if (detail.loading) {
    return (
      <>
        <BackLink />
        <LoadingLines lines={6} label="Loading the trace" />
      </>
    )
  }
  if (detail.gone) {
    return (
      <>
        <BackLink />
        <div className="rounded-lg border bg-card">
          <EmptyState
            icon={WaypointsIcon}
            title="This trace is no longer kept"
            description="The server keeps the newest traces in memory only, and this one has made way for newer ones (or a restart cleared them)."
            action={<Link to="/traces" className={buttonVariants({ variant: 'outline' })}>See the recent traces</Link>}
          />
        </div>
      </>
    )
  }
  if (!detail.view) {
    return (
      <>
        <BackLink />
        <div className="rounded-lg border bg-card">
          <ErrorState title="This trace could not be shown" error={detail.error} onRetry={detail.retry} />
        </div>
      </>
    )
  }
  return (
    <>
      <BackLink />
      <Header view={detail.view} />
      <TraceBody view={detail.view} />
    </>
  )
}
