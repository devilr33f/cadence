import type { QueryClient } from '@tanstack/react-query'
import type { Client } from '@/api/gen/client'
import { getSessionOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'

// a down api fails the guard (and the feed's check) at once, with its error page and Try again, rather than after the
// default retries
export function sessionQueryOptions(api: Client) {
  return {
    ...sharedKey(getSessionOptions({ client: api })),
    retry: false,
  }
}

// the server's word, not the cache's: after a 401 or a 1008 the cached answer may be the one that just ended
export async function checkSession(queryClient: QueryClient, api: Client): Promise<boolean> {
  const session = await queryClient.fetchQuery({
    ...sessionQueryOptions(api),
    staleTime: 0,
  })
  return session.authenticated
}
