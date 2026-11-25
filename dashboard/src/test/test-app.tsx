import type { AnyRouter } from '@tanstack/react-router'
import type { JSX } from 'react'
import type { FakeRoute } from './fake-api'
import type { RouterContext } from '@/router-context'
import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, createRootRouteWithContext, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import { render } from '@testing-library/react'
import { vi } from 'vitest'
import { createApiClient } from '@/api/client'
import { createQueryClient } from '@/api/query-client'
import { AuthedLayout } from '@/components/authed-layout'
import { LayoutError } from '@/components/layout-error'
import { LoginPage } from '@/components/login/login-page'
import { requireSession, skipLoginWhenSignedIn, validateLoginSearch } from '@/lib/auth-guard'
import { createReturnToLogin } from '@/lib/sign-out'
import { fakeApi, TEST_BASE_URL } from './fake-api'

// the app's own guard, login page and signed-in layout on a memory router, against a fake api; pages are paths under
// the signed-in layout (their route ids are the app's own: /_authed/devices/$id for '/devices/$id')
export function renderApp({ path, routes, home = () => <h1>Home</h1>, pages = {} }: {
  path: string
  routes: Record<string, FakeRoute>
  home?: () => JSX.Element
  pages?: Record<string, () => JSX.Element>
}) {
  // the router resets the scroll on each navigation; jsdom has no layout to scroll
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  const server = fakeApi(routes)
  const queryClient = createQueryClient()
  let router: AnyRouter | undefined
  const returnToLogin = createReturnToLogin(() => router!, queryClient)
  const onUnauthorized = vi.fn(() => {
    void returnToLogin()
  })
  const api = createApiClient({ baseUrl: TEST_BASE_URL, fetch: server.fetch, onUnauthorized })

  const root = createRootRouteWithContext<RouterContext>()({ component: Outlet })
  const login = createRoute({
    getParentRoute: () => root,
    path: '/login',
    validateSearch: validateLoginSearch,
    beforeLoad: skipLoginWhenSignedIn,
    component: LoginPage,
  })
  const authed = createRoute({
    getParentRoute: () => root,
    id: '_authed',
    beforeLoad: requireSession,
    component: AuthedLayout,
    errorComponent: LayoutError,
  })
  const children = Object.entries({
    '/': home,
    '/devices': () => <h1>Devices</h1>,
    ...pages,
  }).map(([to, component]) => createRoute({
    getParentRoute: () => authed,
    path: to,
    component,
  }))

  router = createRouter({
    routeTree: root.addChildren([
      login,
      authed.addChildren(children),
    ]),
    history: createMemoryHistory({
      initialEntries: [path],
    }),
    context: { queryClient, api, returnToLogin },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return {
    router,
    server,
    queryClient,
    onUnauthorized,
  }
}
