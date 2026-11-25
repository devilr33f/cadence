import type { TurnView } from '@/api/gen'
import { useInfiniteQuery } from '@tanstack/react-query'
import { MessagesSquareIcon } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { MoreButton } from '@/components/data/more-button'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { turnsQueryOptions } from '@/lib/conversations'
import { useTimeZone } from '@/lib/time-zone'
import { ConversationActions } from './conversation-actions'
import { turnColumns } from './turn-columns'
import { TurnDrawer } from './turn-drawer'
import { turnStamp } from './turn-words'

// a device's turns as the server stored them, newest first, a page at a time; read only while this tab is open (the
// page's loader never asks for them) and refreshed as each of its turns ends
export function Conversations({ deviceId, deviceName }: { deviceId: string, deviceName: string }) {
  const api = useApi()
  const timeZone = useTimeZone()
  const turns = useInfiniteQuery(turnsQueryOptions(api, deviceId))
  const rows = useMemo(() => turns.data?.pages.flatMap(page => page.turns), [turns.data])
  const columns = useMemo(() => turnColumns(timeZone), [timeZone])
  const [shown, setShown] = useState<TurnView>()
  const [open, setOpen] = useState(false)
  const tableRef = useRef<HTMLDivElement>(null)
  // the row the focus moves to once an older page lands: the first of it, as Load older may go with the last page
  const focusRowRef = useRef<number>(undefined)

  useEffect(() => {
    const index = focusRowRef.current
    if (index === undefined || rows === undefined || rows.length <= index)
      return
    focusRowRef.current = undefined
    tableRef.current?.querySelectorAll<HTMLElement>('[data-turn-open]')[index]?.focus()
  }, [rows])

  async function loadOlder() {
    if (turns.isFetchingNextPage || rows === undefined)
      return
    focusRowRef.current = rows.length
    const result = await turns.fetchNextPage()
    if (result.isError)
      focusRowRef.current = undefined
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">Each turn as the server stored it, newest first. Turns are kept 14 days.</p>
        <ConversationActions deviceId={deviceId} />
      </div>
      <div ref={tableRef}>
        <DataTable
          caption={`${deviceName} conversations`}
          columns={columns}
          rows={rows}
          rowKey={turn => turn.turnId}
          status={rows
            ? 'ready'
            : turns.isError ? 'error' : 'loading'}
          error={<ErrorState title="The conversations could not be listed" error={turns.error} onRetry={() => void turns.refetch()} />}
          empty={<EmptyState icon={MessagesSquareIcon} title="No conversations yet" description="Each time the speaker is spoken to, or speaks up on its own, the turn shows here." />}
          rowActions={turn => (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              data-turn-open=""
              aria-label={`Open the turn of ${turnStamp(turn, timeZone)}`}
              onClick={() => {
                setShown(turn)
                setOpen(true)
              }}
            >
              Open
            </Button>
          )}
        />
      </div>
      {turns.isFetchNextPageError && <p role="alert" className="text-sm text-destructive">{`The older turns could not be listed. ${describeError(turns.error)}`}</p>}
      {turns.hasNextPage && (
        <MoreButton busy={turns.isFetchingNextPage} onMore={() => void loadOlder()}>Load older</MoreButton>
      )}
      <TurnDrawer deviceId={deviceId} turn={shown} open={open} onOpenChange={setOpen} />
    </div>
  )
}
