import type { ApiClientOptions } from './client'
import type { Client } from './gen/client'
import process from 'node:process'
import { API_PATH, createApiClient } from './client'

const VARIABLE = 'CADENCE_API_INTERNAL'

// a hung api must not hold a page's render: each ssr call gives up after this long (the browser's own calls do not)
export const SSR_TIMEOUT_MS = 10_000

export interface ServerApiClientOptions extends Omit<ApiClientOptions, 'baseUrl'> {
  env?: Record<string, string | undefined>
  timeoutMs?: number
}

// the ssr server's client: the document's servers entry is relative, so it calls the server on the internal network
// (CADENCE_API_INTERNAL, an origin such as http://cadence-server:8080) with the incoming request's cookie. one per
// request, never shared, so no request's cookie reaches another's call
export function createServerApiClient({ env = process.env, timeoutMs = SSR_TIMEOUT_MS, fetch, ...options }: ServerApiClientOptions = {}): Client {
  return createApiClient({
    ...options,
    baseUrl: `${internalOrigin(env[VARIABLE])}${API_PATH}`,
    fetch: withTimeout(fetch ?? ((input, init) => globalThis.fetch(input, init)), timeoutMs),
  })
}

function withTimeout(fetch: typeof globalThis.fetch, ms: number): typeof globalThis.fetch {
  return (input, init) => {
    const request = new Request(input, init)
    return fetch(new Request(request, {
      signal: AbortSignal.any([
        request.signal,
        AbortSignal.timeout(ms),
      ]),
    }))
  }
}

// errors name the variable, never its value
function internalOrigin(value: string | undefined): string {
  if (!value)
    throw new Error(`${VARIABLE} is not set`)
  let url: URL
  try {
    url = new URL(value)
  }
  catch {
    throw new Error(`${VARIABLE} must be an http(s) origin`)
  }
  const bare = url.pathname === '/' && !url.search && !url.hash && !url.username && !url.password
  if ((url.protocol !== 'http:' && url.protocol !== 'https:') || !bare)
    throw new Error(`${VARIABLE} must be an http(s) origin, without a path, query or credentials`)
  return url.origin
}
