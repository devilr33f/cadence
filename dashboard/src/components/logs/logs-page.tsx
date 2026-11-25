import type { LogFilterField } from '@/lib/logs'
import { keepPreviousData, useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCwIcon } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { ApiError } from '@/api/error'
import { useApi } from '@/api/use-api'
import { PageHeader } from '@/components/data/page-header'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { useDebounced } from '@/hooks/use-debounced'
import { useDeviceNames } from '@/hooks/use-device-names'
import { devicesQueryOptions } from '@/lib/devices'
import { filterError, keepNewestPage, logQuery, logsQueryOptions, NO_FILTER, readNewest } from '@/lib/logs'
import { useTimeZone } from '@/lib/time-zone'
import { LiveTail } from './live-tail'
import { LogFilters } from './log-filters'
import { LogHistory } from './log-history'

// a filter typed key by key asks the server once it pauses
const SETTLE_MS = 300

const FIELDS: readonly LogFilterField[] = [
  'device',
  'source',
  'level',
  'q',
  'since',
  'until',
]

// the field a refusal names (`/query/<field>`), if it is one of the filter's
function refusedField(error: unknown): LogFilterField | undefined {
  if (!(error instanceof ApiError) || error.status !== 422)
    return undefined
  const field = error.path?.replace(/^\/query\//, '')
  return FIELDS.find(f => f === field)
}

export function LogsPage() {
  const api = useApi()
  const timeZone = useTimeZone()
  const names = useDeviceNames()
  const devices = useQuery(devicesQueryOptions(api))
  const [filter, setFilter] = useState(NO_FILTER)
  const [live, setLive] = useState(false)
  const settled = useDebounced(filter, SETTLE_MS)
  // the range is read in the household's zone, which the page has before it lets a range be typed
  const zone = timeZone ?? 'UTC'
  const settledError = filterError(settled, zone)
  const query = useMemo(() => logQuery(settled, zone), [settled, zone])
  // while the tail shows, the stored lines are not read; turning it off reads them again
  const queryClient = useQueryClient()
  const options = logsQueryOptions(api, query)
  const history = useInfiniteQuery({
    ...options,
    enabled: !live && settledError === undefined,
    placeholderData: keepPreviousData,
  })

  const ownError = filterError(filter, zone)
  const refused = refusedField(history.error)
  const errors = {
    ...(refused && { [refused]: `The server refused this filter (${(history.error as ApiError).error}).` }),
    ...(ownError && { [ownError.field]: ownError.message }),
  }
  const liveId = useId()
  const sorted = useMemo(() => [
    ...(devices.data ?? []),
  ].sort((a, b) => a.name.localeCompare(b.name)), [devices.data])

  return (
    <>
      <PageHeader
        title="Logs"
        description="Every speaker's lines and the server's own, as stored. Lines are kept for the log retention setting's days."
        actions={(
          <>
            {!live && (
              <Button type="button" variant="ghost" aria-disabled={history.isFetching || undefined} onClick={() => !history.isFetching && void readNewest(queryClient, options.queryKey)}>
                <RefreshCwIcon aria-hidden="true" />
                Read the newest
              </Button>
            )}
            <label className="flex h-8 items-center gap-2 rounded-md px-2 text-sm font-medium">
              <Switch
                aria-labelledby={liveId}
                checked={live}
                onCheckedChange={(on) => {
                  // back from the tail, the stored lines (disabled while it showed, so no read is on its way) are
                  // read again from their newest page alone
                  if (!on)
                    keepNewestPage(queryClient, options.queryKey)
                  setLive(on)
                }}
              />
              <span id={liveId}>Live</span>
            </label>
          </>
        )}
      />
      <div className="flex flex-col gap-4">
        <LogFilters filter={filter} onChange={setFilter} errors={errors} devices={sorted} zone={timeZone} live={live} />
        {live
          ? <LiveTail filter={settled} names={names} timeZone={timeZone} />
          : <LogHistory history={history} names={names} timeZone={timeZone} filtered={Object.keys(query).length > 0} />}
      </div>
    </>
  )
}
