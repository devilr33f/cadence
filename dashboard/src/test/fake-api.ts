import { vi } from 'vitest'

export const TEST_BASE_URL = 'http://dashboard.test/api/v1'

// a route answers one "METHOD /path" of the api (the path after /api/v1)
export type FakeRoute = (request: Request) => Response | Promise<Response>

export function fakeApi(routes: Record<string, FakeRoute>) {
  const requests: Request[] = []
  const fetch = vi.fn(async (input: RequestInfo | URL) => {
    const request = input instanceof Request
      ? input
      : new Request(input)
    requests.push(request)
    const path = new URL(request.url).pathname.replace(/^\/api\/v1/, '')
    const route = routes[`${request.method} ${path}`]
    return route
      ? route(request)
      : Response.json({ error: 'not_found' }, { status: 404 })
  })
  const calls = (key: string) => requests.filter((r) => {
    const path = new URL(r.url).pathname.replace(/^\/api\/v1/, '')
    return `${r.method} ${path}` === key
  }).length
  return { fetch, requests, calls }
}

export function json(body: unknown, status = 200, headers?: Record<string, string>): FakeRoute {
  return () => Response.json(body, { status, headers })
}

export function noContent(): FakeRoute {
  return () => new Response(null, { status: 204 })
}

export const signedIn = json({ authenticated: true, expiresAt: '2026-12-12T00:00:00Z' })
export const signedOut = json({ authenticated: false })
