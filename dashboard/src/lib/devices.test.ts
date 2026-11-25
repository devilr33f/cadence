import { QueryObserver } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { createApiClient } from '@/api/client'
import { listMemoriesInfiniteOptions, listMemoriesOptions, listTasksOptions } from '@/api/gen/@tanstack/react-query.gen'
import { createQueryClient } from '@/api/query-client'
import { sharedKey } from '@/api/shared-key'
import { OTHER } from '@/test/device-detail'
import { fakeApi, json, TEST_BASE_URL } from '@/test/fake-api'
import { turnQueryOptions, turnsQueryOptions } from './conversations'
import {
  boardsQueryOptions,
  deviceMediaQueryOptions,
  deviceQueryOptions,
  deviceSettingsQueryOptions,
  devicesQueryOptions,
  forgetDeletedElsewhere,
  forgetDevice,
  prefetchDeviceList,
  prefetchDevicePage,
  readsDevice,
} from './devices'
import { logsQueryOptions } from './logs'
import { DEFAULT_WINDOW, deviceMetricsQueryOptions, deviceProcessesQueryOptions } from './metrics'
import { settingsQueryOptions } from './time-zone'

const ID = '0193a71e-55c0-7e2b-b1a4-6f0d93c2e7aa'

function setup(routes: Parameters<typeof fakeApi>[0]) {
  const server = fakeApi(routes)
  const api = createApiClient({ baseUrl: TEST_BASE_URL, fetch: server.fetch })
  return {
    server,
    api,
    queryClient: createQueryClient(),
  }
}

describe('the devices pages\' loaders', () => {
  it('put the list and the boards in the cache', async () => {
    const { api, queryClient } = setup({
      'GET /devices': json([]),
      'GET /boards': json([
        { id: 'sim', name: 'Simulator' },
      ]),
    })
    await prefetchDeviceList(queryClient, api)
    expect(queryClient.getQueryData(devicesQueryOptions(api).queryKey)).toEqual([])
    expect(queryClient.getQueryData(boardsQueryOptions(api).queryKey)).toEqual([
      { id: 'sim', name: 'Simulator' },
    ])
  })

  it('put a device\'s detail, settings, media and the boards in the cache, and never its conversations', async () => {
    const { server, api, queryClient } = setup({
      [`GET /devices/${ID}`]: json({ id: ID }),
      [`GET /devices/${ID}/settings`]: json({ language: 'en' }),
      [`GET /devices/${ID}/media`]: json({ state: 'idle' }),
      'GET /boards': json([]),
    })
    await prefetchDevicePage(queryClient, api, ID)
    expect(queryClient.getQueryData(deviceQueryOptions(api, ID).queryKey)).toEqual({ id: ID })
    expect(queryClient.getQueryData(deviceSettingsQueryOptions(api, ID).queryKey)).toEqual({ language: 'en' })
    expect(queryClient.getQueryData(deviceMediaQueryOptions(api, ID).queryKey)).toEqual({ state: 'idle' })
    expect(server.requests.map(r => new URL(r.url).pathname).filter(path => path.includes('/turns'))).toEqual([])
  })

  it('leave a failure to the page: no throw, no retry', async () => {
    const { server, api, queryClient } = setup({
      [`GET /devices/${ID}`]: json({ error: 'unavailable' }, 503),
      [`GET /devices/${ID}/settings`]: json({}),
      [`GET /devices/${ID}/media`]: json({ state: 'idle' }),
      'GET /boards': json([]),
    })
    await expect(prefetchDevicePage(queryClient, api, ID)).resolves.toBeUndefined()
    expect(server.calls(`GET /devices/${ID}`)).toBe(1)
    expect(queryClient.getQueryState(deviceQueryOptions(api, ID).queryKey)?.status).toBe('error')
  })
})

describe('a deleted device\'s queries', () => {
  const api = createApiClient({ baseUrl: TEST_BASE_URL })

  it('are every query that names it, in its path or as a filter', () => {
    const keys = [
      deviceQueryOptions(api, ID).queryKey,
      deviceSettingsQueryOptions(api, ID).queryKey,
      deviceMediaQueryOptions(api, ID).queryKey,
      turnsQueryOptions(api, ID).queryKey,
      turnQueryOptions(api, ID, 't1').queryKey,
      deviceMetricsQueryOptions(api, ID, 'cpu', DEFAULT_WINDOW).queryKey,
      deviceProcessesQueryOptions(api, ID).queryKey,
      logsQueryOptions(api, { device: ID, level: 'warn' }).queryKey,
      sharedKey(listMemoriesInfiniteOptions({
        client: api,
        query: { scope: 'device', deviceId: ID },
      })).queryKey,
      sharedKey(listTasksOptions({
        client: api,
        query: { device: ID, limit: 20 },
      })).queryKey,
    ]
    for (const key of keys)
      expect(readsDevice(key, ID), JSON.stringify(key)).toBe(true)
  })

  it('are none of the household\'s or another device\'s', () => {
    const keys = [
      devicesQueryOptions(api).queryKey,
      boardsQueryOptions(api).queryKey,
      settingsQueryOptions(api).queryKey,
      deviceQueryOptions(api, OTHER).queryKey,
      deviceMetricsQueryOptions(api, OTHER, 'cpu', DEFAULT_WINDOW).queryKey,
      logsQueryOptions(api, {}).queryKey,
      logsQueryOptions(api, { device: OTHER }).queryKey,
    ]
    for (const key of keys)
      expect(readsDevice(key, ID), JSON.stringify(key)).toBe(false)
  })
})

describe('forgetting a deleted device', () => {
  const api = createApiClient({ baseUrl: TEST_BASE_URL })
  const list = devicesQueryOptions(api).queryKey
  // the household's lists the delete changed: the device's own memories went, and its tasks lost their origin
  const memories = sharedKey(listMemoriesOptions({ client: api })).queryKey
  const tasks = sharedKey(listTasksOptions({ client: api })).queryKey

  function cached() {
    const queryClient = createQueryClient()
    queryClient.setQueryData(list, [
      { id: ID },
      { id: OTHER },
    ] as never)
    queryClient.setQueryData(memories, [] as never)
    queryClient.setQueryData(tasks, [] as never)
    queryClient.setQueryData(deviceQueryOptions(api, ID).queryKey, { id: ID } as never)
    queryClient.setQueryData(deviceMediaQueryOptions(api, ID).queryKey, { state: 'idle' } as never)
    queryClient.setQueryData(deviceQueryOptions(api, OTHER).queryKey, { id: OTHER } as never)
    return queryClient
  }

  const reads = (queryClient: ReturnType<typeof createQueryClient>, id: string) => queryClient.getQueryCache().getAll().filter(q => readsDevice(q.queryKey, id))

  it('drops its queries, takes it out of the list, and marks the household\'s lists stale', async () => {
    const queryClient = cached()
    await forgetDevice(queryClient, api, ID)
    expect(reads(queryClient, ID)).toEqual([])
    expect(reads(queryClient, OTHER)).toHaveLength(1)
    expect(queryClient.getQueryData<{ id: string }[]>(list)?.map(d => d.id)).toEqual([OTHER])
    for (const key of [list, memories, tasks])
      expect(queryClient.getQueryState(key)?.isInvalidated, JSON.stringify(key)).toBe(true)
  })

  it('deleted elsewhere, keeps a query a page still observes, which would be read again if dropped', async () => {
    const queryClient = cached()
    // as a page's useQuery holds it, never fetching here
    const observer = new QueryObserver(queryClient, {
      queryKey: deviceMediaQueryOptions(api, ID).queryKey,
      enabled: false,
    })
    const unsubscribe = observer.subscribe(() => {})
    await forgetDeletedElsewhere(queryClient, api, ID)
    expect(reads(queryClient, ID).map(q => (q.queryKey[0] as { _id: string })._id)).toEqual(['getDeviceMedia'])
    expect(queryClient.getQueryData<{ id: string }[]>(list)?.map(d => d.id)).toEqual([OTHER])
    unsubscribe()
  })
})

describe('the devices list', () => {
  it('is read again every 2 minutes as a floor under the presence events', () => {
    expect(devicesQueryOptions(createApiClient({ baseUrl: TEST_BASE_URL })).refetchInterval).toBe(120_000)
  })
})
