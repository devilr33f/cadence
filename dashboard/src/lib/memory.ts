import type { QueryClient } from '@tanstack/react-query'
import type { CreateMemoryRequest, EditMemoryRequest, MemoryJson } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { queryOptions, useQuery, useQueryClient } from '@tanstack/react-query'
import { approveMemory, createMemory, deleteMemory, editMemory, listMemories, rejectMemory } from '@/api/gen'
import { listDeviceCompactionsOptions, listDeviceLearningOptions, listMemoriesInfiniteOptions, listMemoriesOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'
import { devicesQueryOptions } from './devices'

export type MemoryKind = CreateMemoryRequest['kind']

export const MEMORY_KINDS = [
  'preference',
  'fact',
  'alias',
  'lesson',
] as const satisfies readonly MemoryKind[]

// a page of the memories table (the server's default page)
export const MEMORY_PAGE = 50
// the suggestions in one read: the server's most; it keeps at most 100 pending a scope
export const SUGGESTIONS_LIMIT = 200
// the learning log and the compactions: the server's default page, then more up to its most
export const LOG_PAGE = 20
export const LOG_MAX = 200

// the suggestion count on the rail, which follows no topic of its own away from the memory page
const SUGGESTIONS_REFRESH_MS = 60_000

// what the memories table narrows to, on the server: a scope (a device's), a kind
export interface MemoryFilter {
  scope?: 'global' | 'device'
  deviceId?: string
  kind?: MemoryKind
}

// the active memories, a page at a time by offset; pending ones are the suggestions'
export function memoriesQueryOptions(api: Client, filter: MemoryFilter) {
  return {
    ...sharedKey(listMemoriesInfiniteOptions({
      client: api,
      query: { status: 'active', limit: MEMORY_PAGE, ...filter },
    })),
    initialPageParam: 0,
    getNextPageParam: (last: MemoryJson[], pages: MemoryJson[][]) => last.length < MEMORY_PAGE
      ? undefined
      : pages.reduce((count, page) => count + page.length, 0),
  }
}

// what learning proposes while approval is on: new memories, and updates or deletions of active ones
export function suggestionsQueryOptions(api: Client) {
  return sharedKey(listMemoriesOptions({
    client: api,
    query: { status: 'pending', limit: SUGGESTIONS_LIMIT },
  }))
}

// a scope's active memories whole, to show the text an update proposal would overwrite: no route reads one memory by
// id. a scope keeps at most 300 active, so a few reads of the server's most cover it; the key starts as every memories
// query does, so the agent topic and the actions refresh it too
const SCOPE_READS = 5

export function scopeMemoriesQueryOptions(api: Client, scope: string, deviceId: string | null) {
  return queryOptions({
    queryKey: [
      {
        _id: 'listMemories',
        whole: true,
        scope,
        deviceId,
      },
    ],
    queryFn: async ({ signal }) => {
      const all: MemoryJson[] = []
      for (let read = 0; read < SCOPE_READS; read++) {
        const { data } = await listMemories({
          client: api,
          query: {
            status: 'active',
            scope: scope === 'device'
              ? 'device'
              : 'global',
            ...(deviceId !== null && { deviceId }),
            limit: SUGGESTIONS_LIMIT,
            offset: read * SUGGESTIONS_LIMIT,
          },
          signal,
          throwOnError: true,
        })
        all.push(...data)
        if (data.length < SUGGESTIONS_LIMIT)
          break
      }
      return all
    },
  })
}

// the rail's count: a read that came back full is at least that many
export function suggestionsBadge(count: number): { count: number, more?: true, label: string } {
  if (count >= SUGGESTIONS_LIMIT)
    return { count: SUGGESTIONS_LIMIT, more: true, label: `${SUGGESTIONS_LIMIT} or more suggestions to review` }
  if (count === 0)
    return { count, label: 'no suggestions to review' }
  return {
    count,
    label: count === 1
      ? '1 suggestion to review'
      : `${count} suggestions to review`,
  }
}

export function useSuggestionCount(): number {
  const api = useApi()
  const { data } = useQuery({
    ...suggestionsQueryOptions(api),
    select: list => list.length,
    refetchInterval: SUGGESTIONS_REFRESH_MS,
  })
  return data ?? 0
}

export function learningQueryOptions(api: Client, deviceId: string, limit: number) {
  return sharedKey(listDeviceLearningOptions({
    client: api,
    path: { id: deviceId },
    query: { limit },
  }))
}

export function compactionsQueryOptions(api: Client, deviceId: string, limit: number) {
  return sharedKey(listDeviceCompactionsOptions({
    client: api,
    path: { id: deviceId },
    query: { limit },
  }))
}

// in the cache before the page renders, the speakers' names too; a failure is the page's to show (and retry), not the navigation's to wait for
export async function prefetchMemoryPage(queryClient: QueryClient, api: Client) {
  await Promise.all([
    queryClient.prefetchInfiniteQuery({
      ...memoriesQueryOptions(api, {}),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...suggestionsQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...devicesQueryOptions(api),
      retry: false,
    }),
  ])
}

// every memories query (the table's pages, the suggestions), whatever its filter
const MEMORIES_KEY = [
  { _id: 'listMemories' },
]

// called directly, not as mutations: memory texts stay in the query cache alone (rule 8). each refreshes the memories
// after any answer, since one whose answer was lost may still have been applied; the agent topic refreshes them too
export function useMemoryActions() {
  const api = useApi()
  const queryClient = useQueryClient()
  async function refreshing(action: () => Promise<unknown>) {
    try {
      await action()
    }
    finally {
      void queryClient.invalidateQueries({ queryKey: MEMORIES_KEY })
    }
  }
  return {
    create: (body: CreateMemoryRequest) => refreshing(() => createMemory({ client: api, body, throwOnError: true })),
    edit: (id: string, body: EditMemoryRequest) => refreshing(() => editMemory({
      client: api,
      path: { id },
      body,
      throwOnError: true,
    })),
    remove: (id: string) => refreshing(() => deleteMemory({
      client: api,
      path: { id },
      throwOnError: true,
    })),
    approve: (id: string) => refreshing(() => approveMemory({
      client: api,
      path: { id },
      throwOnError: true,
    })),
    reject: (id: string) => refreshing(() => rejectMemory({
      client: api,
      path: { id },
      throwOnError: true,
    })),
    // a pending memory is edited in place, then approved as edited
    editAndApprove: (id: string, body: EditMemoryRequest) => refreshing(async () => {
      if (Object.keys(body).length > 0) {
        await editMemory({
          client: api,
          path: { id },
          body,
          throwOnError: true,
        })
      }
      await approveMemory({
        client: api,
        path: { id },
        throwOnError: true,
      })
    }),
  }
}
