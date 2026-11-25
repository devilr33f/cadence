import type { ApiError } from './error'
import type { ClientOptions } from './gen'
import type { Client } from './gen/client'
import { retryAfterSeconds, toApiError } from './error'
import { createClient, createConfig } from './gen/client'

export { ApiError } from './error'

export const API_PATH = '/api/v1'

// M7a's cookie rule: a cookie request other than GET, HEAD or OPTIONS carries this header
const CSRF_HEADER = 'X-Cadence-CSRF'
const CSRF_FREE = new Set(['GET', 'HEAD'])

// the session operations answer for themselves: a wrong password (login) and a session already gone (logout) are their
// callers' to word, and reading the session never answers 401
const SESSION_URL = '/session'

export interface ApiClientOptions {
  // absolute on the ssr server; the browser keeps the relative default, so it stays on the page's origin
  baseUrl?: string
  // ssr only: the incoming request's Cookie header, forwarded as is
  cookie?: string
  fetch?: typeof globalThis.fetch
  onUnauthorized?: (error: ApiError) => void
}

export function createApiClient(options: ApiClientOptions = {}): Client {
  const client = createClient(createConfig<ClientOptions>({
    baseUrl: options.baseUrl ?? API_PATH,
    credentials: 'same-origin',
    // the api never redirects: one would be a proxy's page, and on the ssr server it would carry the cookie along
    redirect: 'error',
    // its answers hold transcripts, memories, task text and media metadata, which no browser cache may keep on disk
    // (rule 8); the server sends no cache-control on most of them
    cache: 'no-store',
    ...(options.fetch && { fetch: options.fetch }),
    ...(options.cookie && {
      headers: { Cookie: options.cookie },
    }),
  }))

  client.interceptors.request.use((request) => {
    if (!CSRF_FREE.has(request.method))
      request.headers.set(CSRF_HEADER, '1')
    return request
  })

  client.interceptors.error.use((error, response, _request, sent) => {
    if (!response || response.ok)
      return error
    const apiError = toApiError(response.status, error, retryAfterSeconds(response.headers.get('Retry-After')))
    if (response.status === 401 && sent.url !== SESSION_URL)
      options.onUnauthorized?.(apiError)
    return apiError
  })

  return client
}
