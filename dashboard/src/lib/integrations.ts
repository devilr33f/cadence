import type { QueryClient } from '@tanstack/react-query'
import type { Client } from '@/api/gen/client'
import { busyQueryOptions } from './busy'
import { devicesQueryOptions } from './devices'
import { homeAssistantQueryOptions } from './home-assistant'
import { mcpServersQueryOptions } from './mcp'
import { providersQueryOptions } from './providers'
import { subscriptionsQueryOptions } from './subscriptions'
import { taskKindsQueryOptions } from './tasks'

// what the integrations page reads first, in the cache before it renders; what depends on it (a running client's
// entities, a server's tools) follows in the browser. a failure is the page's to show (and retry)
export async function prefetchIntegrations(queryClient: QueryClient, api: Client) {
  await Promise.all([
    queryClient.prefetchQuery({
      ...homeAssistantQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...mcpServersQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...taskKindsQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...devicesQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...subscriptionsQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...busyQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...providersQueryOptions(api),
      retry: false,
    }),
  ])
}
