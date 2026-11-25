import { afterEach, expect, it, vi } from 'vitest'
import { getSession } from './gen'
import { requestApiClient } from './request-client'

// outside the start compiler the isomorphic function runs its server side, as the ssr server does
vi.mock('@tanstack/react-start/server', () => ({
  getRequestHeader: (name: string) => name === 'cookie'
    ? 'cadence_session=s1; cadence_browser=b1'
    : undefined,
}))

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

it('checks the session on the ssr server with the incoming request\'s cookie', async () => {
  vi.stubEnv('CADENCE_API_INTERNAL', 'http://cadence-server:8080')
  const fetch = vi.fn(async (_request: Request) => Response.json({ authenticated: true }))
  vi.stubGlobal('fetch', fetch)

  const { data } = await getSession({
    client: requestApiClient({}),
  })

  expect(data).toEqual({ authenticated: true })
  const request = fetch.mock.calls[0]?.[0]
  expect(request?.url).toBe('http://cadence-server:8080/api/v1/session')
  expect(request?.headers.get('Cookie')).toBe('cadence_session=s1; cadence_browser=b1')
})
