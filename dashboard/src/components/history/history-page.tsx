import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { HeartIcon, HistoryIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { useApi } from '@/api/use-api'
import { MoreButton } from '@/components/data/more-button'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { devicesQueryOptions } from '@/lib/devices'
import { favoriteAsHistory, favoritesQueryOptions, HISTORY_MAX, HISTORY_PAGE, historyQueryOptions, useRateMusicSong } from '@/lib/history'
import { useTimeZone } from '@/lib/time-zone'
import { HistoryTable } from './history-table'

export function HistoryPage() {
  const api = useApi()
  const timeZone = useTimeZone()
  const [deviceId, setDeviceId] = useState('')
  const [limit, setLimit] = useState(HISTORY_PAGE)
  const [view, setView] = useState<'history' | 'favorites'>('history')
  const list = useQuery({
    ...historyQueryOptions(api, deviceId, limit),
    placeholderData: keepPreviousData,
    enabled: view === 'history',
  })
  const favorites = useQuery({
    ...favoritesQueryOptions(api),
    placeholderData: keepPreviousData,
    enabled: view === 'favorites',
  })
  const devices = useQuery(devicesQueryOptions(api))
  const rating = useRateMusicSong(api)
  const filterId = useId()
  const viewId = useId()
  const canMore = view === 'history' && list.data !== undefined && limit < HISTORY_MAX
    && (list.isPlaceholderData || list.data.items.length >= limit)
  const favoriteView = view === 'favorites'
  const active = favoriteView ? favorites : list
  const entries = favoriteView
    ? favorites.data?.items.map(favoriteAsHistory)
    : list.data?.items

  return (
    <>
      <PageHeader title="History" description="Recent plays are kept for 14 days. Music favorites stay until you change them." />
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={viewId}>Show</Label>
            <NativeSelect id={viewId} size="sm" value={view} onChange={event => setView(event.target.value as typeof view)}>
              <NativeSelectOption value="history">Recent plays</NativeSelectOption>
              <NativeSelectOption value="favorites">Favorites</NativeSelectOption>
            </NativeSelect>
          </div>
          {!favoriteView && (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={filterId}>Device</Label>
              <NativeSelect
                id={filterId}
                size="sm"
                value={deviceId}
                onChange={(event) => {
                  setDeviceId(event.target.value)
                  setLimit(HISTORY_PAGE)
                }}
              >
                <NativeSelectOption value="">All devices</NativeSelectOption>
                {(devices.data ?? []).map(device => <NativeSelectOption key={device.id} value={device.id}>{device.name}</NativeSelectOption>)}
              </NativeSelect>
            </div>
          )}
        </div>
        {rating.isError && <ErrorState title="The rating could not be saved" error={rating.error} />}
        <HistoryTable
          entries={entries}
          timeZone={timeZone}
          favorites={favoriteView}
          caption={favoriteView ? 'Favorite songs' : 'Recently played'}
          busy={rating.isPending}
          onRate={(entry, feedback) => {
            if (!entry.provider || !entry.providerSongId)
              return
            rating.mutate({
              client: api,
              path: { provider: entry.provider, songId: entry.providerSongId },
              body: { feedback },
            })
          }}
          status={active.isError
            ? 'error'
            : active.isPending
              ? 'loading'
              : 'ready'}
          error={<ErrorState title={favoriteView ? 'Favorites could not be listed' : 'The history could not be listed'} error={active.error} onRetry={() => void active.refetch()} />}
          empty={favoriteView
            ? <EmptyState icon={HeartIcon} title="No favorites yet" description="Like a song here or by voice to keep it in Cadence favorites." />
            : <EmptyState icon={HistoryIcon} title="Nothing played yet" description={deviceId ? 'This device has no play history in the last 14 days.' : 'Tracks, videos and streams appear here after they play on a device.'} />}
        />
        {canMore && <MoreButton busy={list.isFetching} onMore={() => setLimit(HISTORY_MAX)}>Show more history</MoreButton>}
      </div>
    </>
  )
}
