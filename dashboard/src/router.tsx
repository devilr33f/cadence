import type { AnyRouter } from '@tanstack/react-router'
import { createRouter } from '@tanstack/react-router'
import { setupRouterSsrQueryIntegration } from '@tanstack/react-router-ssr-query'
import { createQueryClient } from './api/query-client'
import { requestApiClient } from './api/request-client'
import { createReturnToLogin } from './lib/sign-out'
import { routeTree } from './routeTree.gen'

// a router, its query cache and its api client per ssr request, so one reader's data or cookie never reaches another's
// page; one of each per tab in the browser
export function getRouter() {
  const queryClient = createQueryClient()
  const made: { router?: AnyRouter } = {}
  const returnToLogin = createReturnToLogin(() => made.router!, queryClient)
  const api = requestApiClient({
    onUnauthorized: () => {
      void returnToLogin()
    },
  })
  const router = createRouter({
    routeTree,
    context: { queryClient, api, returnToLogin },
    scrollRestoration: true,
  })
  made.router = router
  setupRouterSsrQueryIntegration({
    router,
    queryClient,
    // a failed query is fetched again in the browser rather than carried in the page, error and all
    dehydrateOptions: {
      shouldDehydrateQuery: query => query.state.status !== 'error',
    },
  })
  return router
}
