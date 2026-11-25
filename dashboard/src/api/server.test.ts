import { describe, expect, it, vi } from 'vitest'
import { createDevice, listDevices } from './gen'
import { createServerApiClient } from './server'

function fakeServer() {
  const requests: Request[] = []
  const fetch = vi.fn(async (input: RequestInfo | URL) => {
    const request = input instanceof Request
      ? input
      : new Request(input)
    requests.push(request)
    return Response.json({})
  })
  return { fetch, requests }
}

describe('createServerApiClient', () => {
  it('calls the server at CADENCE_API_INTERNAL with the incoming cookie', async () => {
    const server = fakeServer()
    const client = createServerApiClient({
      cookie: 'cadence_session=s1',
      env: { CADENCE_API_INTERNAL: 'http://cadence-server:8080' },
      fetch: server.fetch,
    })

    await listDevices({ client })
    await createDevice({
      client,
      body: { name: 'kitchen', model: 'sim' },
    })

    expect(server.requests.map(r => [
      r.url,
      r.headers.get('cookie'),
      r.headers.get('X-Cadence-CSRF'),
    ])).toEqual([
      ['http://cadence-server:8080/api/v1/devices', 'cadence_session=s1', null],
      ['http://cadence-server:8080/api/v1/devices', 'cadence_session=s1', '1'],
    ])
    expect(server.requests[0]?.redirect).toBe('error')
    // the timeout's wrapper keeps it: node's fetch caches nothing, the mode only says so
    expect(server.requests.map(r => r.cache)).toEqual(['no-store', 'no-store'])
  })

  it('sends no cookie when the incoming request had none', async () => {
    const server = fakeServer()
    const client = createServerApiClient({
      env: { CADENCE_API_INTERNAL: 'http://cadence-server:8080/' },
      fetch: server.fetch,
    })

    await listDevices({ client })

    expect(server.requests[0]?.url).toBe('http://cadence-server:8080/api/v1/devices')
    expect(server.requests[0]?.headers.has('cookie')).toBe(false)
  })

  it('gives up on a call the server does not answer in time, so a hung api never holds the page', async () => {
    const signals: AbortSignal[] = []
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const request = input as Request
      signals.push(request.signal)
      return new Promise<Response>((_, reject) => {
        request.signal.addEventListener('abort', () => reject(request.signal.reason instanceof Error
          ? request.signal.reason
          : new Error('aborted')))
      })
    })
    const client = createServerApiClient({
      env: { CADENCE_API_INTERNAL: 'http://cadence-server:8080' },
      fetch,
      timeoutMs: 20,
    })

    await expect(listDevices({ client, throwOnError: true })).rejects.toMatchObject({ name: 'TimeoutError' })
    expect(signals).toHaveLength(1)
    expect(signals[0]?.aborted).toBe(true)
  })

  it.each([
    undefined,
    '',
    'cadence-server:8080',
    'ftp://cadence-server',
    'http://cadence-server:8080/api',
    'http://cadence-server:8080?x=secret-value',
    'http://user:secret-value@cadence-server:8080',
  ])('refuses CADENCE_API_INTERNAL=%s, naming the variable and never its value', (value) => {
    let thrown: unknown
    try {
      createServerApiClient({
        env: { CADENCE_API_INTERNAL: value },
      })
    }
    catch (error) {
      thrown = error
    }
    expect(thrown).toBeInstanceOf(Error)
    const message = (thrown as Error).message
    expect(message).toContain('CADENCE_API_INTERNAL')
    if (value)
      expect(message).not.toContain(value)
    expect(message).not.toContain('secret-value')
  })
})
