import { QueryClient } from '@tanstack/react-query'
import { ApiError } from './error'

// the server's answer to a request it refused stays the same on a retry: only a failed fetch or a 5xx is tried again
function retryTransient(failures: number, error: unknown): boolean {
  return failures < 3 && !(error instanceof ApiError && error.status < 500)
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: retryTransient,
        // what the ssr server fetched is fresh enough not to be fetched again as the page hydrates; the live feed
        // invalidates what changes sooner
        staleTime: 30_000,
      },
    },
  })
}
