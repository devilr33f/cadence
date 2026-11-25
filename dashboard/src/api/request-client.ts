import type { ApiClientOptions } from './client'
import type { Client } from './gen/client'
import { createIsomorphicFn } from '@tanstack/react-start'
import { getRequestHeader } from '@tanstack/react-start/server'
import { createApiClient } from './client'
import { createServerApiClient } from './server'

type BrowserOptions = Pick<ApiClientOptions, 'onUnauthorized'>

// the client a router gets: on the ssr server one per request with the incoming cookie (a 401 there renders as the
// page's error, the browser's next request goes to the login), in the browser the relative one with the 401 handler
export const requestApiClient = createIsomorphicFn()
  .server((_options: BrowserOptions): Client => createServerApiClient({
    cookie: getRequestHeader('cookie'),
  }))
  .client((options: BrowserOptions): Client => createApiClient(options))
