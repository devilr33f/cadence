import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useId } from 'react'
import { useApi } from '@/api/use-api'
import { ErrorState, LoadingLines } from '@/components/data/states'
import { Card, CardAction, CardContent, CardHeader } from '@/components/ui/card'
import { historyQueryOptions } from '@/lib/history'
import { useTimeZone } from '@/lib/time-zone'
import { HistoryTable } from './history-table'

export function RecentlyPlayed({ deviceId }: { deviceId: string }) {
  const api = useApi()
  const timeZone = useTimeZone()
  const titleId = useId()
  const history = useQuery(historyQueryOptions(api, deviceId, 5))
  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader>
        <h2 id={titleId} className="font-heading text-base font-semibold">Recently played</h2>
        <CardAction><Link to="/history" className="text-sm text-muted-foreground underline-offset-3 hover:text-foreground hover:underline">View all</Link></CardAction>
      </CardHeader>
      <CardContent>
        {history.isPending && <LoadingLines lines={2} label="Loading recently played items" />}
        {history.isError && <ErrorState title="The recent plays could not be read" error={history.error} onRetry={() => void history.refetch()} className="px-0 py-4" />}
        {history.data?.items.length === 0 && <p className="text-sm text-muted-foreground">Nothing played in the last 14 days.</p>}
        {history.data !== undefined && history.data.items.length > 0 && (
          <HistoryTable entries={history.data.items} timeZone={timeZone} showDevice={false} caption="Recently played on this device" />
        )}
      </CardContent>
    </Card>
  )
}
