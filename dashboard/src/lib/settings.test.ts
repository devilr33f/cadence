import { QueryClient } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { createApiClient } from '@/api/client'
import { fakeApi, TEST_BASE_URL } from '@/test/fake-api'
import { devicesQueryOptions } from './devices'
import { homeAssistantQueryOptions } from './home-assistant'
import { mcpServersQueryOptions, mcpToolsQueryOptions } from './mcp'
import { GLOBAL_SCOPE, putSetting } from './settings'
import { taskKindsQueryOptions } from './tasks'
import { settingsQueryOptions } from './time-zone'

it('chains a tab\'s global saves, so the second reads the set only after the first landed and keeps its change', async () => {
  let stored: Record<string, unknown> = { language: 'ru' }
  let land!: () => void
  const firstLanded = new Promise<void>((resolve) => {
    land = resolve
  })
  let puts = 0
  const server = fakeApi({
    'GET /settings/global': () => Response.json(stored),
    'PUT /settings/global': async (request) => {
      puts += 1
      if (puts === 1)
        await firstLanded
      stored = await request.clone().json() as Record<string, unknown>
      return Response.json(stored)
    },
  })
  const api = createApiClient({ baseUrl: TEST_BASE_URL, fetch: server.fetch })
  const queryClient = new QueryClient()

  const first = putSetting(api, queryClient, GLOBAL_SCOPE, 'language', 'en')
  const second = putSetting(api, queryClient, GLOBAL_SCOPE, 'historyTurns', 30)
  await vi.waitFor(() => expect(server.calls('PUT /settings/global')).toBe(1))
  expect(server.calls('GET /settings/global')).toBe(1)

  land()
  await Promise.all([first, second])
  const bodies = await Promise.all(server.requests.filter(r => r.method === 'PUT').map(r => r.clone().json()))
  expect(bodies).toEqual([
    { language: 'en' },
    { language: 'en', historyTurns: 30 },
  ])
})

it('runs the next save after one that failed', async () => {
  const server = fakeApi({
    'GET /settings/global': () => Response.json({}),
    'PUT /settings/global': async (request) => {
      const body = await request.clone().json() as Record<string, unknown>
      return 'language' in body
        ? Response.json({ error: 'settings_invalid', path: '/language' }, { status: 422 })
        : Response.json(body)
    },
  })
  const api = createApiClient({ baseUrl: TEST_BASE_URL, fetch: server.fetch })
  const queryClient = new QueryClient()

  const first = putSetting(api, queryClient, GLOBAL_SCOPE, 'language', 'xx')
  const second = putSetting(api, queryClient, GLOBAL_SCOPE, 'historyTurns', 30)
  await expect(first).rejects.toMatchObject({ status: 422 })
  await expect(second).resolves.toBeUndefined()
  expect(server.calls('PUT /settings/global')).toBe(2)
})

it('reads the set afresh for a save, never joining a read already on its way from before', async () => {
  let stored: Record<string, unknown> = { language: 'ru' }
  let answerOld!: () => void
  const oldAnswered = new Promise<void>((resolve) => {
    answerOld = resolve
  })
  let gets = 0
  const server = fakeApi({
    'GET /settings/global': async () => {
      gets += 1
      const seen = { ...stored }
      if (gets === 1)
        await oldAnswered
      return Response.json(seen)
    },
    'PUT /settings/global': async (request) => {
      stored = await request.clone().json() as Record<string, unknown>
      return Response.json(stored)
    },
  })
  const api = createApiClient({ baseUrl: TEST_BASE_URL, fetch: server.fetch })
  const queryClient = new QueryClient()

  // a focus refetch read the set before another writer's save landed, and is still on its way
  const focusRead = queryClient.fetchQuery({
    ...settingsQueryOptions(api),
    staleTime: 0,
  }).catch(() => undefined)
  await vi.waitFor(() => expect(server.calls('GET /settings/global')).toBe(1))
  stored = { language: 'ru', historyTurns: 30 }

  const put = putSetting(api, queryClient, GLOBAL_SCOPE, 'language', 'en')
  await vi.waitFor(() => expect(server.calls('GET /settings/global')).toBe(2))
  answerOld()
  await Promise.all([put, focusRead])
  expect(await server.requests.find(r => r.method === 'PUT')!.clone().json()).toEqual({ language: 'en', historyTurns: 30 })
})

it('marks stale what the global set feeds on other pages, and only a global save does', async () => {
  const server = fakeApi({
    'GET /settings/global': () => Response.json({}),
    'PUT /settings/global': async request => Response.json(await request.clone().json()),
    'PUT /devices/d1/settings': async request => Response.json(await request.clone().json()),
  })
  const api = createApiClient({ baseUrl: TEST_BASE_URL, fetch: server.fetch })
  const queryClient = new QueryClient()
  // what the integrations page shows (the mcp servers and a server's tools, hermes's kind, home assistant's status)
  // and an unrelated list
  const dependent = [
    mcpServersQueryOptions(api),
    mcpToolsQueryOptions(api, 'notes'),
    taskKindsQueryOptions(api),
    homeAssistantQueryOptions(api),
  ].map(options => options.queryKey)
  const unrelated = devicesQueryOptions(api).queryKey
  for (const key of [...dependent, unrelated])
    queryClient.setQueryData(key, [])
  const stale = (key: readonly unknown[]) => queryClient.getQueryState(key)?.isInvalidated

  await putSetting(api, queryClient, { kind: 'device', deviceId: 'd1' }, 'followUp', null)
  expect(dependent.map(stale)).toEqual([false, false, false, false])

  await putSetting(api, queryClient, GLOBAL_SCOPE, 'mcp', [
    { name: 'notes', instance: 'notes' },
  ])
  expect(dependent.map(stale)).toEqual([true, true, true, true])
  expect(stale(unrelated)).toBe(false)
})
