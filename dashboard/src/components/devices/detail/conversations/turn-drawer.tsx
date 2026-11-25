import type { TurnView } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { useApi } from '@/api/use-api'
import { ErrorState, LoadingLines } from '@/components/data/states'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { turnQueryOptions } from '@/lib/conversations'
import { useTimeZone } from '@/lib/time-zone'
import { TurnFailure } from './turn-failure'
import { TurnMessages } from './turn-messages'
import { isEventTurn, turnStamp, turnSummary } from './turn-words'

// read only while the drawer shows it, and kept a short while after (lib/conversations.ts)
function TurnBody({ deviceId, turn }: { deviceId: string, turn: TurnView }) {
  const api = useApi()
  const detail = useQuery(turnQueryOptions(api, deviceId, turn.turnId))
  if (detail.isPending)
    return <LoadingLines lines={6} label="Loading the turn" />
  if (detail.isError)
    return <ErrorState title="This turn could not be shown" error={detail.error} onRetry={() => void detail.refetch()} />
  const skills = detail.data.skills ?? []
  return (
    <>
      {skills.length > 0 && (
        <p className="mb-4 flex flex-wrap items-baseline gap-x-2 gap-y-1 text-sm">
          <span className="text-muted-foreground">Skills loaded</span>
          {skills.map(name => <span key={name} className="font-mono text-xs font-medium">{name}</span>)}
        </p>
      )}
      <TurnMessages messages={detail.data.messages} event={isEventTurn(turn)} />
      {detail.data.failure && <TurnFailure failure={detail.data.failure} />}
    </>
  )
}

// one turn's messages as stored, beside the list; it gives the focus back to the row's Open when it closes
export function TurnDrawer({ deviceId, turn, open, onOpenChange }: {
  deviceId: string
  turn?: TurnView
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const timeZone = useTimeZone()
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="gap-0 data-[side=right]:w-full data-[side=right]:sm:max-w-xl">
        {turn && (
          <>
            <SheetHeader className="border-b pr-12">
              <SheetTitle className="text-lg font-semibold">{`Turn of ${turnStamp(turn, timeZone)}`}</SheetTitle>
              <SheetDescription>{turnSummary(turn)}</SheetDescription>
            </SheetHeader>
            <div className="min-h-0 flex-1 overflow-y-auto p-4">
              <TurnBody deviceId={deviceId} turn={turn} />
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
