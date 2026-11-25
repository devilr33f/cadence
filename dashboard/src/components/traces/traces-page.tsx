import { useQuery } from '@tanstack/react-query'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { WaypointsIcon } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useApi } from '@/api/use-api'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState } from '@/components/data/states'
import { useDeviceNames } from '@/hooks/use-device-names'
import { devicesQueryOptions } from '@/lib/devices'
import { useTimeZone } from '@/lib/time-zone'
import { filterRows, NO_TRACE_FILTER } from '@/lib/traces/filter'
import { StreamsPanel } from './streams-panel'
import { TraceFilters } from './trace-filters'
import { TraceStatsBar } from './trace-stats'
import { TraceTable } from './trace-table'
import { useTraceList } from './use-trace-list'

const COUNT = new Intl.NumberFormat('en')

export function TracesPage() {
  const api = useApi()
  const timeZone = useTimeZone()
  const names = useDeviceNames()
  const devices = useQuery(devicesQueryOptions(api))
  const { device = '' } = useSearch({ from: '/_authed/traces/' })
  const navigate = useNavigate({ from: '/traces/' })
  const [filter, setFilter] = useState(NO_TRACE_FILTER)
  const list = useTraceList(device)
  const rows = useMemo(() => filterRows(list.rows, filter), [list.rows, filter])
  const sorted = useMemo(() => [
    ...(devices.data ?? []),
  ].sort((a, b) => a.name.localeCompare(b.name)), [devices.data])
  const listId = useId()
  const filtered = filter !== NO_TRACE_FILTER && (filter.liveOnly || filter.errorsOnly || filter.name.trim() !== '')

  return (
    <>
      <PageHeader
        title="Traces"
        description="What the server and the speakers did, span by span, as it happens. Running traces change in place; the server keeps the newest finished ones."
      />
      <div className="flex flex-col gap-8">
        <TraceStatsBar />
        <StreamsPanel streams={list.streams} names={names} />
        <section aria-labelledby={listId} className="flex flex-col gap-3">
          <h2 id={listId} className="text-lg font-semibold">Recent traces</h2>
          <TraceFilters
            device={device}
            onDevice={next => void navigate({
              search: next === ''
                ? {}
                : { device: next },
              replace: true,
            })}
            devices={sorted}
            filter={filter}
            onChange={setFilter}
          />
          {list.dropped > 0 && (
            <p role="status" className="text-sm text-warning">
              {`The live feed held back ${COUNT.format(list.dropped)} span events; the list read the traces they changed again.`}
            </p>
          )}
          <TraceTable
            rows={rows}
            names={names}
            timeZone={timeZone}
            status={list.status}
            error={<ErrorState title="The traces could not be listed" error={list.error} onRetry={list.retry} />}
            empty={filtered || device !== ''
              ? <EmptyState icon={WaypointsIcon} title="No trace matches" description="Change the filters above; the list holds the newest traces the server keeps." />
              : <EmptyState icon={WaypointsIcon} title="No traces yet" description="A trace shows here the moment its first span starts: a voice turn, an API call, a device's work." />}
          />
        </section>
      </div>
    </>
  )
}
