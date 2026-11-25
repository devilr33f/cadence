import type { QueryClient } from '@tanstack/react-query'
import type { PutModeRequest } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { deleteMode, putMode } from '@/api/gen'
import { listModesOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

export function modesQueryOptions(api: Client) {
  return sharedKey(listModesOptions({ client: api }))
}

// in the cache before the page renders; a failure is the page's to show (and retry), not the navigation's to wait for
export async function prefetchModes(queryClient: QueryClient, api: Client) {
  await queryClient.prefetchQuery({
    ...modesQueryOptions(api),
    retry: false,
  })
}

// no live topic covers the modes, so each change refreshes the list itself: a put after any answer, since one whose
// answer was lost may still have landed
export function useModeActions() {
  const api = useApi()
  const queryClient = useQueryClient()
  const refresh = () => queryClient.invalidateQueries({ queryKey: modesQueryOptions(api).queryKey })
  return {
    // creates or replaces the whole mode: the server has no create of its own
    async put(name: string, body: PutModeRequest) {
      try {
        await putMode({
          client: api,
          path: { name },
          body,
          throwOnError: true,
        })
      }
      finally {
        void refresh()
      }
    },
    // the list is read again after the confirmation closed, so its row is not unmounted with the dialog still open
    async remove(name: string) {
      await deleteMode({
        client: api,
        path: { name },
        throwOnError: true,
      })
      void refresh()
    },
  }
}
