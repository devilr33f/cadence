import type { Client } from '@/api/gen/client'
import { getTraceOptions, getTraceStatsOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'

// the list's first read, and how often the header's figures are read again while the page shows (no event carries them)
export const TRACES_PAGE = 100
export const STATS_EVERY_MS = 2000
// a since read reads this many changed traces at a time, and at most this many pages in a row
export const SINCE_PAGE = 200
export const SINCE_PAGES = 5

export function traceStatsQueryOptions(api: Client) {
  return sharedKey(getTraceStatsOptions({ client: api }))
}

export function traceQueryOptions(api: Client, id: string) {
  return sharedKey(getTraceOptions({
    client: api,
    path: { id },
  }))
}
