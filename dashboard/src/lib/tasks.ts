import type { QueryClient } from '@tanstack/react-query'
import type { TaskJson } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { approveTask, cancelTask, replyToTask } from '@/api/gen'
import { getTaskOptions, inspectTaskOptions, listTaskKindsOptions, listTasksOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'
import { devicesQueryOptions } from './devices'

// the list's page (the server's default), then more up to its most
export const TASKS_PAGE = 50
export const TASKS_MAX = 200

const TERMINAL: ReadonlySet<string> = new Set(['done', 'failed', 'cancelled'])

// a task the server still runs, or lost track of (unknown): it may be cancelled
export function isActive(task: Pick<TaskJson, 'status'>): boolean {
  return !TERMINAL.has(task.status)
}

// a task's open question; the server sends null while it asks nothing
export type Question = NonNullable<TaskJson['question']>

export function tasksQueryOptions(api: Client, status: string, limit: number) {
  return sharedKey(listTasksOptions({
    client: api,
    query: {
      ...(status !== '' && { status }),
      limit,
    },
  }))
}

export function taskQueryOptions(api: Client, id: string) {
  return sharedKey(getTaskOptions({
    client: api,
    path: { id },
  }))
}

// the kind's live view (its progress, its question, its result)
export function inspectQueryOptions(api: Client, id: string) {
  return sharedKey(inspectTaskOptions({
    client: api,
    path: { id },
  }))
}

// the live view with the run's whole transcript, under a key of its own: the tasks topic reads it again only once the
// run ended (live/invalidation.ts), not on each step's event
export const TRANSCRIPT_QUERY = 'inspectTaskTranscript'

export function transcriptQueryOptions(api: Client, id: string) {
  const options = sharedKey(inspectTaskOptions({
    client: api,
    path: { id },
    query: { detail: 'transcript' },
  }))
  return {
    ...options,
    queryKey: [
      { ...options.queryKey[0], _id: TRANSCRIPT_QUERY },
    ] as typeof options.queryKey,
  }
}

export function taskKindsQueryOptions(api: Client) {
  return sharedKey(listTaskKindsOptions({ client: api }))
}

// in the cache before the page renders, the speakers' names too; a failure is the page's to show (and retry)
export async function prefetchTasks(queryClient: QueryClient, api: Client) {
  await Promise.all([
    queryClient.prefetchQuery({
      ...tasksQueryOptions(api, '', TASKS_PAGE),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...devicesQueryOptions(api),
      retry: false,
    }),
  ])
}

export async function prefetchTask(queryClient: QueryClient, api: Client, id: string) {
  await Promise.all([
    queryClient.prefetchQuery({
      ...taskQueryOptions(api, id),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...devicesQueryOptions(api),
      retry: false,
    }),
  ])
}

// every query a task's answer may change, whatever its id or filter; the tasks topic refreshes them too
const TASK_KEYS = [
  [
    { _id: 'listTasks' },
  ],
  [
    { _id: 'getTask' },
  ],
  [
    { _id: 'inspectTask' },
  ],
  [
    { _id: TRANSCRIPT_QUERY },
  ],
]

// called directly, not as mutations: a reply's words stay out of the mutation cache (rule 8)
export function useTaskActions(id: string) {
  const api = useApi()
  const queryClient = useQueryClient()
  async function refreshing(action: () => Promise<unknown>) {
    try {
      await action()
    }
    finally {
      for (const queryKey of TASK_KEYS)
        void queryClient.invalidateQueries({ queryKey })
    }
  }
  return {
    reply: (message: string, token: string) => refreshing(() => replyToTask({
      client: api,
      path: { id },
      body: { message, token },
      throwOnError: true,
    })),
    approve: (decision: 'once' | 'deny', token: string) => refreshing(() => approveTask({
      client: api,
      path: { id },
      body: { decision, token },
      throwOnError: true,
    })),
    cancel: () => refreshing(() => cancelTask({
      client: api,
      path: { id },
      throwOnError: true,
    })),
  }
}
