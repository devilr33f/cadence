import { describe, expect, it, vi } from 'vitest'
import { ApiError, createApiClient } from './client'
import { createDevice, createSession, deleteSession, deleteToken, editMemory, listDevices, putGlobalSettings, sendDeviceCommand } from './gen'

const baseUrl = 'http://dashboard.test/api/v1'

function fakeServer(answer: (request: Request) => Response = () => Response.json({})) {
  const requests: Request[] = []
  const fetch = vi.fn(async (input: RequestInfo | URL) => {
    const request = input instanceof Request
      ? input
      : new Request(input)
    requests.push(request)
    return answer(request)
  })
  return { fetch, requests }
}

function errorBody(status: number, body: object) {
  return () => Response.json(body, { status })
}

describe('createApiClient', () => {
  it('sends the csrf header on every method but GET and HEAD', async () => {
    const server = fakeServer()
    const client = createApiClient({ baseUrl, fetch: server.fetch })

    await createDevice({
      client,
      body: { name: 'kitchen', model: 'sim' },
    })
    await putGlobalSettings({ client, body: {} })
    await editMemory({
      client,
      path: { id: '1' },
      body: {},
    })
    await deleteToken({
      client,
      path: { name: 'ha' },
    })
    await listDevices({ client })
    await client.head({ url: '/session' })

    expect(server.requests.map(r => [
      r.method,
      r.headers.get('X-Cadence-CSRF'),
    ])).toEqual([
      ['POST', '1'],
      ['PUT', '1'],
      ['PATCH', '1'],
      ['DELETE', '1'],
      ['GET', null],
      ['HEAD', null],
    ])
  })

  it('keeps cookies same-origin, defaults to the relative api path and refuses redirects', async () => {
    const server = fakeServer()
    const client = createApiClient({ baseUrl, fetch: server.fetch })

    await listDevices({ client })

    expect(client.getConfig().credentials).toBe('same-origin')
    expect(server.requests[0]?.credentials).toBe('same-origin')
    expect(server.requests[0]?.redirect).toBe('error')
    expect(server.requests[0]?.url).toBe(`${baseUrl}/devices`)
    expect(createApiClient().getConfig().baseUrl).toBe('/api/v1')
  })

  it('keeps every answer out of the browser\'s http cache (transcripts, memories, task text: rule 8)', async () => {
    const server = fakeServer()
    const client = createApiClient({ baseUrl, fetch: server.fetch })

    await listDevices({ client })
    await putGlobalSettings({ client, body: {} })

    expect(server.requests.map(r => r.cache)).toEqual(['no-store', 'no-store'])
  })

  it('turns an ErrorBody into an ApiError with its detail and path', async () => {
    const failed = createApiClient({
      baseUrl,
      fetch: fakeServer(errorBody(409, { error: 'command_failed', detail: 'no_session' })).fetch,
    })
    const thrown = await sendDeviceCommand({
      client: failed,
      path: { id: 'kitchen', cap: 'audio.output', name: 'set_level' },
      body: { level: 10 },
      throwOnError: true,
    }).catch((error: unknown) => error)
    expect(thrown).toBeInstanceOf(ApiError)
    expect(thrown).toMatchObject({ status: 409, error: 'command_failed', detail: 'no_session' })

    const invalid = createApiClient({
      baseUrl,
      fetch: fakeServer(errorBody(422, { error: 'invalid', path: '/body/name' })).fetch,
    })
    const { error } = await createDevice({
      client: invalid,
      body: { name: '', model: 'sim' },
    })
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ status: 422, error: 'invalid', path: '/body/name' })
    expect((error as ApiError).detail).toBeUndefined()
  })

  it('names a body that is no ErrorBody unexpected_response, keeping its status', async () => {
    const client = createApiClient({
      baseUrl,
      fetch: fakeServer(() => new Response('<html>bad gateway</html>', { status: 502 })).fetch,
    })
    const { error } = await listDevices({ client })
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ status: 502, error: 'unexpected_response' })
  })

  it('calls onUnauthorized on a 401 only', async () => {
    const onUnauthorized = vi.fn()
    const expired = createApiClient({
      baseUrl,
      fetch: fakeServer(errorBody(401, { error: 'unauthorized' })).fetch,
      onUnauthorized,
    })
    await listDevices({ client: expired })
    expect(onUnauthorized).toHaveBeenCalledOnce()
    expect(onUnauthorized.mock.calls[0]?.[0]).toMatchObject({ status: 401, error: 'unauthorized' })

    const forbidden = createApiClient({
      baseUrl,
      fetch: fakeServer(errorBody(403, { error: 'forbidden' })).fetch,
      onUnauthorized,
    })
    await listDevices({ client: forbidden })
    expect(onUnauthorized).toHaveBeenCalledOnce()
  })

  it('leaves a 401 of the session operations to their callers', async () => {
    const onUnauthorized = vi.fn()
    const client = createApiClient({
      baseUrl,
      fetch: fakeServer(errorBody(401, { error: 'unauthorized' })).fetch,
      onUnauthorized,
    })
    const login = await createSession({
      client,
      body: { password: 'not-the-password' },
    })
    const logout = await deleteSession({ client })
    expect(login.error).toMatchObject({ status: 401, error: 'unauthorized' })
    expect(logout.error).toMatchObject({ status: 401, error: 'unauthorized' })
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('reads the wait of a 429 from Retry-After', async () => {
    const limited = (retryAfter?: string) => createApiClient({
      baseUrl,
      fetch: fakeServer(() => Response.json({ error: 'rate_limited' }, {
        status: 429,
        headers: retryAfter === undefined
          ? {}
          : { 'Retry-After': retryAfter },
      })).fetch,
    })
    const login = (client: ReturnType<typeof createApiClient>) => createSession({
      client,
      body: { password: 'x' },
    })

    expect((await login(limited('37'))).error).toMatchObject({ status: 429, error: 'rate_limited', retryAfter: 37 })
    expect((await login(limited())).error).toMatchObject({ retryAfter: undefined })
    expect((await login(limited('soon'))).error).toMatchObject({ retryAfter: undefined })
  })

  it('passes a failed fetch through unchanged', async () => {
    const offline = new TypeError('fetch failed')
    const onUnauthorized = vi.fn()
    const client = createApiClient({
      baseUrl,
      fetch: vi.fn(async () => Promise.reject(offline)),
      onUnauthorized,
    })
    await expect(listDevices({ client, throwOnError: true })).rejects.toBe(offline)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })
})
