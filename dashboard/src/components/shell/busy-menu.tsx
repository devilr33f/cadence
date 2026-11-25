import type { DeviceView } from '@/api/gen'
import type { Topic } from '@/lib/live/topics'
import { useQuery } from '@tanstack/react-query'
import { BellIcon, BellOffIcon } from 'lucide-react'
import { describeError } from '@/api/describe-error'
import { useApi } from '@/api/use-api'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuGroup, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { toast } from '@/components/ui/toast-manager'
import { BUSY_REFRESH_MS, busyQueryOptions, busySummary, DND_DURATIONS, dndSignal, REASON_TEXT, useBusyActions } from '@/lib/busy'
import { devicesQueryOptions } from '@/lib/devices'
import { stampText } from '@/lib/format-time'
import { useLiveTopics } from '@/lib/live/use-live'
import { useTimeZone } from '@/lib/time-zone'

// busy_changed: a speaker became busy or free, or its reason changed
const TOPICS: readonly Topic[] = ['presence']

function nameOf(devices: readonly DeviceView[] | undefined, id: string): string {
  return devices?.find(device => device.id === id)?.name ?? 'A speaker'
}

// the header's busy state: which speakers hold what nobody asked for and why, and the owner's do-not-disturb
export function BusyMenu() {
  useLiveTopics(TOPICS)
  const api = useApi()
  const timeZone = useTimeZone()
  const busy = useQuery({
    ...busyQueryOptions(api),
    refetchInterval: BUSY_REFRESH_MS,
  })
  const devices = useQuery(devicesQueryOptions(api)).data
  const actions = useBusyActions()
  // a read that failed shows nothing it does not know: the toggle stays, the state is not guessed
  const view = busy.data
  const summary = busySummary(view)
  const dnd = dndSignal(view)
  const busyDevices = view?.devices ?? []

  async function run(action: () => Promise<unknown>, failed: string) {
    try {
      await action()
    }
    catch (error) {
      toast.add({
        type: 'error',
        title: failed,
        description: describeError(error),
      })
    }
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="sm" aria-label={summary.full} data-busy={busyDevices.length > 0 || undefined} />}>
        {busyDevices.length > 0
          ? <BellOffIcon aria-hidden="true" className="text-warning" />
          : <BellIcon aria-hidden="true" />}
        <span
          aria-hidden="true"
          className={busyDevices.length > 0
            ? 'text-warning max-sm:sr-only'
            : 'max-sm:sr-only'}
        >
          {summary.short}
        </span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64">
        <DropdownMenuGroup>
          <DropdownMenuLabel>
            {busyDevices.length === 0
              ? 'No speaker is busy'
              : 'Holding updates'}
          </DropdownMenuLabel>
          {busyDevices.map(device => (
            <DropdownMenuLabel key={device.deviceId} className="flex justify-between gap-3 text-sm font-normal text-foreground">
              <span className="truncate">{nameOf(devices, device.deviceId)}</span>
              <span className="shrink-0 text-muted-foreground">{REASON_TEXT[device.reason]}</span>
            </DropdownMenuLabel>
          ))}
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuLabel>
            {dnd?.expiresAt
              ? `Do not disturb until ${stampText(dnd.expiresAt, timeZone)}`
              : 'Do not disturb every speaker'}
          </DropdownMenuLabel>
          {dnd && (
            <DropdownMenuItem onClick={() => void run(() => actions.clear(dnd.source), 'Do not disturb was not ended')}>
              <BellIcon aria-hidden="true" />
              End do not disturb
            </DropdownMenuItem>
          )}
          {DND_DURATIONS.map(duration => (
            <DropdownMenuItem
              key={duration.minutes}
              onClick={() => void run(() => actions.startDnd(duration.minutes), 'Do not disturb was not set')}
            >
              <BellOffIcon aria-hidden="true" />
              {dnd
                ? `Do not disturb for ${duration.label} from now`
                : `Do not disturb for ${duration.label}`}
            </DropdownMenuItem>
          ))}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
