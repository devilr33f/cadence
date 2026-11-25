import type { BusySignal, BusyView } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/api/error'
import { deleteBusy, putBusy } from '@/api/gen'
import { getBusyOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

export type BusyReason = BusySignal['reason']

// the dashboard's own do-not-disturb (deploy/server/README.md, Busy mode); the voice tool's is `voice`, and its `off`
// clears this one too
export const DND_SOURCE = 'dashboard'

// a signal expires on its own; the read follows it even with no event (an expiry publishes busy_changed, but a page
// that lost the feed meanwhile still shows the truth within this)
export const BUSY_REFRESH_MS = 60_000

export const REASON_TEXT: Record<BusyReason, string> = {
  call: 'In a call',
  meeting: 'In a meeting',
  screenshare: 'Screensharing',
  dnd: 'Do not disturb',
}

// the durations offered for the dashboard's do-not-disturb, in minutes; the server takes at most a day
export const DND_DURATIONS: readonly { minutes: number, label: string }[] = [
  { minutes: 30, label: '30 minutes' },
  { minutes: 60, label: '1 hour' },
  { minutes: 120, label: '2 hours' },
  { minutes: 480, label: '8 hours' },
  { minutes: 1440, label: '24 hours' },
]

export function busyQueryOptions(api: Client) {
  return sharedKey(getBusyOptions({ client: api }))
}

export function dndSignal(view: BusyView | undefined): BusySignal | undefined {
  return view?.signals.find(signal => signal.source === DND_SOURCE)
}

// a home assistant source is the server's own (no expiry, not to be cleared here)
export function isHomeAssistantSource(source: string): boolean {
  return source.startsWith('ha:')
}

// where a signal came from, in words; the source itself is shown beside it as an id
export function sourceText(source: string): string {
  if (source === DND_SOURCE)
    return 'Dashboard'
  if (source === 'voice')
    return 'Voice'
  if (isHomeAssistantSource(source))
    return 'Home Assistant'
  return 'API'
}

// the trigger's words, said in full to a screen reader
export function busySummary(view: BusyView | undefined): { short: string, full: string } {
  const count = view?.devices.length ?? 0
  if (count === 0)
    return { short: 'Not busy', full: 'Busy mode: no speaker is busy' }
  const speakers = count === 1
    ? '1 speaker'
    : `${count} speakers`
  if (dndSignal(view))
    return { short: 'Do not disturb', full: `Busy mode: do not disturb, ${speakers} holding updates` }
  return { short: 'Busy', full: `Busy mode: ${speakers} busy, holding updates` }
}

const BUSY_KEYS = [
  [
    { _id: 'getBusy' },
  ],
]

export function useBusyActions() {
  const api = useApi()
  const queryClient = useQueryClient()
  async function refreshing(action: () => Promise<unknown>) {
    try {
      await action()
    }
    finally {
      for (const queryKey of BUSY_KEYS)
        void queryClient.invalidateQueries({ queryKey })
    }
  }
  return {
    // every speaker: the owner's do-not-disturb, as the voice tool sets it
    startDnd: (minutes: number) => refreshing(() => putBusy({
      client: api,
      path: { source: DND_SOURCE },
      body: { reason: 'dnd', devices: null, ttlSeconds: minutes * 60 },
      throwOnError: true,
    })),
    // a signal already gone (expired, or cleared by voice) is what was asked for
    clear: (source: string) => refreshing(async () => {
      try {
        await deleteBusy({
          client: api,
          path: { source },
          throwOnError: true,
        })
      }
      catch (error) {
        if (!(error instanceof ApiError && error.status === 404))
          throw error
      }
    }),
  }
}
