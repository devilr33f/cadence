import type { TurnPage } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { compactDeviceConversation, resetDeviceConversation } from '@/api/gen'
import { getDeviceTurnOptions, listDeviceCompactionsOptions, listDeviceTurnsInfiniteOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

// the server's default page, newest first
export const TURNS_PAGE = 20

// transcripts live in the query cache alone, and only a little while after they leave the page: never persisted,
// never fetched by the ssr server (the tab is not prefetched), so never carried in a page
export const TRANSCRIPT_GC_MS = 30_000

export function turnsQueryOptions(api: Client, deviceId: string) {
  return {
    ...sharedKey(listDeviceTurnsInfiniteOptions({
      client: api,
      path: { id: deviceId },
      query: { limit: TURNS_PAGE },
    })),
    // the first page has no cursor: its page parameter only repeats the device, so `before` stays out of the request
    initialPageParam: {
      path: { id: deviceId },
    },
    getNextPageParam: (page: TurnPage) => page.next,
    gcTime: TRANSCRIPT_GC_MS,
  }
}

export function turnQueryOptions(api: Client, deviceId: string, turnId: string) {
  return {
    ...sharedKey(getDeviceTurnOptions({
      client: api,
      path: { id: deviceId, turnId },
    })),
    gcTime: TRANSCRIPT_GC_MS,
  }
}

// direct actions, not persisted mutations: transcript queries stay in memory
export function useConversationActions(deviceId: string) {
  const api = useApi()
  const queryClient = useQueryClient()
  return {
    async reset() {
      await resetDeviceConversation({
        client: api,
        path: { id: deviceId },
        throwOnError: true,
      })
      void queryClient.invalidateQueries({ queryKey: turnsQueryOptions(api, deviceId).queryKey })
    },
    async compact() {
      await compactDeviceConversation({
        client: api,
        path: { id: deviceId },
        throwOnError: true,
      })
      void queryClient.invalidateQueries({
        queryKey: listDeviceCompactionsOptions({
          client: api,
          path: { id: deviceId },
        }).queryKey,
      })
    },
  }
}
