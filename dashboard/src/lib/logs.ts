import type { InfiniteData, QueryClient, QueryKey } from '@tanstack/react-query'
import type { LogPage, LogPageParam } from './log-paging'
import type { ListLogsData } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import type { Severity } from '@/components/data/severity-label'
import { listLogs } from '@/api/gen'
import { listLogsQueryKey } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { nextLogPage, unseenLines } from './log-paging'
import { zonedToInstant } from './zoned-time'

// a page of stored lines, and the most the page holds before it asks for a narrower filter
export const LOGS_PAGE = 200
export const LOGS_MAX = 2000

// lines may carry what third-party programs wrote: kept in the query cache alone, and only a little while after they
// leave the page; never fetched by the ssr server (the page has no loader), so never carried in a page
export const LOGS_GC_MS = 30_000

export type LogLevel = '' | 'info' | 'warn' | 'error'

export interface LogFilter {
  device: string
  source: string
  level: LogLevel
  q: string
  // wall times in the household's zone, as their datetime-local fields hold them
  since: string
  until: string
}

export const NO_FILTER: LogFilter = {
  device: '',
  source: '',
  level: '',
  q: '',
  since: '',
  until: '',
}

export type LogFilterField = keyof LogFilter

// the lowest severity number of each level (otlp's ranges), as the server reads `level`
const LEVEL_FLOOR: Record<Exclude<LogLevel, ''>, number> = { info: 9, warn: 13, error: 17 }

// the server's byte bounds of the text filters
const MAX_Q = 256
const MAX_SOURCE = 64

const bytes = (text: string) => new TextEncoder().encode(text).length

export function logQuery(filter: LogFilter, zone: string): NonNullable<ListLogsData['query']> {
  const source = filter.source.trim()
  const q = filter.q.trim()
  const since = zonedToInstant(filter.since, zone)
  const until = zonedToInstant(filter.until, zone)
  return {
    ...(filter.device !== '' && { device: filter.device }),
    ...(source !== '' && { source }),
    ...(filter.level !== '' && { level: filter.level }),
    ...(q !== '' && { q }),
    ...(since !== undefined && { since }),
    ...(until !== undefined && { until }),
  }
}

// what the server would refuse, said on its field before anything is asked
export function filterError(filter: LogFilter, zone: string): { field: LogFilterField, message: string } | undefined {
  if (bytes(filter.source.trim()) > MAX_SOURCE)
    return { field: 'source', message: 'At most 64 bytes.' }
  if (bytes(filter.q.trim()) > MAX_Q)
    return { field: 'q', message: 'At most 256 bytes.' }
  const { since, until } = logQuery(filter, zone)
  if (since !== undefined && until !== undefined && Date.parse(until) <= Date.parse(since))
    return { field: 'until', message: 'Until must be after From.' }
  return undefined
}

// a device logs at four levels; a number between them counts as the one below (as the device's own pipeline reads it)
export function severityOf(severity: number): Severity {
  if (severity >= 17)
    return 'error'
  if (severity >= 13)
    return 'warn'
  if (severity >= 9)
    return 'info'
  return 'debug'
}

export interface LineFields {
  deviceId?: string
  source: string
  severity: number
  body: string
}

// a live line against the filter, read as the server reads a stored one: the device, the source exactly, the lowest
// level, the text in any case
export function matchesLine(line: LineFields, filter: LogFilter): boolean {
  const source = filter.source.trim()
  const q = filter.q.trim().toLowerCase()
  return (filter.device === '' || line.deviceId === filter.device)
    && (source === '' || line.source === source)
    && (filter.level === '' || line.severity >= LEVEL_FLOOR[filter.level])
    && (q === '' || line.body.toLowerCase().includes(q))
}

// stored lines, newest first, a page at a time: each older page ends where the one before it did, and keeps the lines
// that share the oldest line's time
export function logsQueryOptions(api: Client, query: NonNullable<ListLogsData['query']>) {
  const params = { ...query, limit: LOGS_PAGE }
  const { queryKey } = sharedKey({
    queryKey: listLogsQueryKey({
      client: api,
      query: params,
    }),
  })
  return {
    queryKey,
    queryFn: async ({ pageParam, signal }: { pageParam: LogPageParam, signal: AbortSignal }): Promise<LogPage> => {
      const { data } = await listLogs({
        client: api,
        query: {
          ...params,
          ...(pageParam.until !== undefined && { until: pageParam.until }),
        },
        signal,
        throwOnError: true,
      })
      const lines = data.logs ?? []
      return {
        lines: unseenLines(lines, pageParam),
        raw: lines.length,
        param: pageParam,
      }
    },
    initialPageParam: {} as LogPageParam,
    getNextPageParam: (_last: LogPage, pages: LogPage[]) => nextLogPage(pages, LOGS_PAGE),
    gcTime: LOGS_GC_MS,
    // a read of a day's lines may take the server seconds: read again on purpose (the page's refresh), not on focus
    refetchOnWindowFocus: false,
    staleTime: 0,
  }
}

// keep the stored lines' newest page alone: an infinite query's refetch reads every loaded page in turn (up to ten,
// each a search the server may take seconds over), so the older ones are dropped before it reads again
export function keepNewestPage(queryClient: QueryClient, queryKey: QueryKey) {
  queryClient.setQueryData<InfiniteData<LogPage, LogPageParam>>(queryKey, data => data && {
    pages: data.pages.slice(0, 1),
    pageParams: data.pageParams.slice(0, 1),
  })
}

// read the newest page again, alone
export async function readNewest(queryClient: QueryClient, queryKey: QueryKey) {
  await queryClient.cancelQueries({ queryKey, exact: true })
  keepNewestPage(queryClient, queryKey)
  await queryClient.refetchQueries({ queryKey, exact: true })
}
