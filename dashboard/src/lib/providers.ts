import type { QueryClient } from '@tanstack/react-query'
import type { CreateProviderRequest, TestResult, UpdateProviderRequest } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { createProvider, deleteProvider, testProvider, updateProvider } from '@/api/gen'
import { listProviderModulesOptions, listProvidersOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

export function providersQueryOptions(api: Client) {
  return sharedKey(listProvidersOptions({ client: api }))
}

// the modules the server registers, each with its config schema and its secrets' names: they change only with the
// server's build
export function providerModulesQueryOptions(api: Client) {
  return {
    ...sharedKey(listProviderModulesOptions({ client: api })),
    staleTime: Infinity,
  }
}

// in the cache before the page renders; a failure is the page's to show (and retry), not the navigation's to wait for
export async function prefetchProviders(queryClient: QueryClient, api: Client) {
  await Promise.all([
    queryClient.prefetchQuery({
      ...providersQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...providerModulesQueryOptions(api),
      retry: false,
    }),
  ])
}

// called directly, not as mutations: a create or update carries secrets, and a mutation keeps its variables in the mutation
// cache. no live topic covers providers, so each refreshes the list itself: after any answer, since a write whose
// answer was lost may still have been made
export function useProviderActions() {
  const api = useApi()
  const queryClient = useQueryClient()
  const refresh = () => queryClient.invalidateQueries({ queryKey: providersQueryOptions(api).queryKey })
  return {
    async create(request: CreateProviderRequest) {
      try {
        await createProvider({ client: api, body: request, throwOnError: true })
      }
      finally {
        void refresh()
      }
    },
    async update(name: string, request: UpdateProviderRequest) {
      try {
        await updateProvider({
          client: api,
          path: { name },
          body: request,
          throwOnError: true,
        })
      }
      finally {
        void refresh()
      }
    },
    // the list is read again after the confirmation closed, so its row is not unmounted with the dialog still open
    async remove(name: string) {
      await deleteProvider({
        client: api,
        path: { name },
        throwOnError: true,
      })
      void refresh()
    },
    // the server builds the instance afresh and runs its module's cheapest free call: ok, or a code, never upstream text
    async test(name: string): Promise<TestResult> {
      const { data } = await testProvider({
        client: api,
        path: { name },
        throwOnError: true,
      })
      return data
    },
  }
}
