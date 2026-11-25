/// <reference types="vite/client" />
import type { RouterContext } from '@/router-context'
import { createRootRouteWithContext, Outlet } from '@tanstack/react-router'
import { RootDocument } from '@/components/root-document'
import { THEME_SCRIPT } from '@/lib/theme'
import appCss from '@/styles/app.css?url'

export const Route = createRootRouteWithContext<RouterContext>()({
  head: () => ({
    meta: [
      { charSet: 'utf-8' },
      { name: 'viewport', content: 'width=device-width, initial-scale=1' },
      { title: 'Cadence' },
    ],
    links: [
      { rel: 'stylesheet', href: appCss },
      { rel: 'icon', href: '/favicon.svg', type: 'image/svg+xml' },
    ],
    // before the stylesheet paints anything: the reader's theme, without a flash of the other one
    scripts: [
      { children: THEME_SCRIPT },
    ],
  }),
  // every page is the owner's alone, its dehydrated data included: no shared cache may keep one
  headers: () => ({ 'Cache-Control': 'private, no-store' }),
  shellComponent: RootDocument,
  component: Outlet,
})
