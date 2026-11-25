import { dehydrate, hydrate, QueryClient } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { sessionQueryOptions } from '@/lib/session'
import { settingsQueryOptions } from '@/lib/time-zone'
import { createApiClient } from './client'
import { getDeviceOptions, listDevicesOptions } from './gen/@tanstack/react-query.gen'
import { createServerApiClient } from './server'
import { sharedKey } from './shared-key'

const env = { CADENCE_API_INTERNAL: 'http://cadence-server:8080' }

describe('sharedKey', () => {
  it('gives the ssr server and the browser one key for one request', () => {
    const server = createServerApiClient({ env })
    const browser = createApiClient()

    expect(sessionQueryOptions(server).queryKey).toEqual(sessionQueryOptions(browser).queryKey)
    expect(settingsQueryOptions(server).queryKey).toEqual(settingsQueryOptions(browser).queryKey)
    expect(sharedKey(listDevicesOptions({ client: server })).queryKey)
      .toEqual(sharedKey(listDevicesOptions({ client: browser })).queryKey)
    expect(sharedKey(getDeviceOptions({
      client: server,
      path: { id: 'd1' },
    })).queryKey)
      .toEqual(sharedKey(getDeviceOptions({
        client: browser,
        path: { id: 'd1' },
      })).queryKey)
    // the internal origin never lands in the page's dehydrated state
    expect(JSON.stringify(sessionQueryOptions(server).queryKey)).not.toContain('cadence-server')
  })

  it('still sends each request to its own client\'s base', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(async () => Response.json({ authenticated: true }))
    const server = createServerApiClient({ env, cookie: 'cadence_session=s1', fetch })
    await new QueryClient().fetchQuery(sessionQueryOptions(server))
    const request = fetch.mock.calls[0]?.[0] as Request
    expect(request.url).toBe('http://cadence-server:8080/api/v1/session')
    expect(request.headers.get('Cookie')).toBe('cadence_session=s1')
  })

  it('lets the browser find what the ssr server fetched', async () => {
    const fetch = vi.fn(async () => Response.json({ authenticated: true }))
    const ssr = new QueryClient()
    await ssr.fetchQuery(sessionQueryOptions(createServerApiClient({ env, fetch })))

    const browser = new QueryClient()
    hydrate(browser, JSON.parse(JSON.stringify(dehydrate(ssr))))
    expect(browser.getQueryData(sessionQueryOptions(createApiClient()).queryKey)).toEqual({ authenticated: true })
  })
})
