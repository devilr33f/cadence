import type { QueryClient } from '@tanstack/react-query'
import type { Client } from '@/api/gen/client'

export interface RouterContext {
  queryClient: QueryClient
  // one per ssr request (the incoming cookie forwarded), one per tab in the browser
  api: Client
  // the session ended elsewhere (a 401, the feed's 1008): back to the login, nothing of the session kept
  returnToLogin: () => Promise<void>
}
