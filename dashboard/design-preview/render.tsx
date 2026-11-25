// renders each preview page inside the real shell, on a memory router, to static markup
import type { ReactElement } from 'react'
import type { PreviewPage } from './page'
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import { renderToString } from 'react-dom/server'
import { AppShell } from '@/components/shell/app-shell'
import { SECTION_ROUTES } from '@/components/shell/nav-sections'
import { DeviceDetailScreen, DevicesScreen, StatesScreen } from './screens'

export const SCREENS = [
  {
    id: 'devices',
    label: 'Devices',
    path: '/devices',
    component: DevicesScreen,
  },
  {
    id: 'detail',
    label: 'Device detail',
    path: '/devices/kitchen',
    component: DeviceDetailScreen,
  },
  {
    id: 'states',
    label: 'States',
    path: '/states',
    component: StatesScreen,
  },
] as const

// the preview shows the whole nav, as it will be once every page exists
const ALL_SECTIONS = {
  overview: '/',
  devices: '/devices',
  providers: '/providers',
  modes: '/modes',
  memory: '/memory',
  tasks: '/tasks',
  integrations: '/integrations',
  logs: '/logs',
  metrics: '/metrics',
  admin: '/admin',
} as const

async function render(path: string, Screen: () => ReactElement) {
  Object.assign(SECTION_ROUTES, ALL_SECTIONS)
  const root = createRootRoute({
    component: () => (
      <AppShell feed={{ state: 'live' }} account={{ state: 'signed-in' }}>
        <Outlet />
      </AppShell>
    ),
  })
  const page = createRoute({
    getParentRoute: () => root,
    path,
    component: Screen,
  })
  const router = createRouter({
    routeTree: root.addChildren([page]),
    history: createMemoryHistory({
      initialEntries: [path],
    }),
  })
  // loaded first, nothing suspends: the markup is whole, with no streaming scripts
  await router.load()
  return renderToString(<RouterProvider router={router} />)
}

export async function renderScreens() {
  const pages: PreviewPage[] = []
  for (const { id, label, path, component } of SCREENS)
    pages.push({ id, label, html: await render(path, component) })
  return pages
}
