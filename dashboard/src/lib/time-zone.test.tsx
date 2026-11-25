// @vitest-environment jsdom
import type { RouterContext } from '@/router-context'
import type { FakeRoute } from '@/test/fake-api'
import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, createRootRouteWithContext, createRouter, RouterProvider } from '@tanstack/react-router'
import { cleanup, render, screen } from '@testing-library/react'
import { renderToString } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApiClient } from '@/api/client'
import { createQueryClient } from '@/api/query-client'
import { Timeline } from '@/components/data/timeline'
import { fakeApi, json, TEST_BASE_URL } from '@/test/fake-api'
import { CLOCK_PLACEHOLDER, clockText } from './format-time'
import { useTimeZone } from './time-zone'

afterEach(cleanup)

const AT = '2026-11-12T15:04:12Z'

function Probe() {
  return (
    <Timeline
      label="Activity"
      timeZone={useTimeZone()}
      entries={[
        {
          id: 'e1',
          at: AT,
          kind: 'turn',
          text: 'Woken by voice',
        },
      ]}
    />
  )
}

function app(settings: FakeRoute) {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  const server = fakeApi({ 'GET /settings/global': settings })
  const queryClient = createQueryClient()
  const api = createApiClient({ baseUrl: TEST_BASE_URL, fetch: server.fetch })
  const root = createRootRouteWithContext<RouterContext>()({ component: Probe })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({
      initialEntries: ['/'],
    }),
    context: {
      queryClient,
      api,
      returnToLogin: vi.fn(),
    },
  })
  const tree = (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return { router, tree }
}

describe('the household time zone', () => {
  it('words a time in the given zone, or holds its place while the zone is unknown', () => {
    expect(clockText(AT, 'Asia/Bishkek')).toBe('21:04:12')
    expect(clockText(AT, null)).toBe(CLOCK_PLACEHOLDER)
  })

  it('shows placeholders until the setting loads, then the zone\'s time', async () => {
    let answer!: (response: Response) => void
    const { tree } = app(() => new Promise<Response>((resolve) => {
      answer = resolve
    }))
    render(tree)
    expect((await screen.findByRole('list', { name: 'Activity' })).textContent).toContain(CLOCK_PLACEHOLDER)

    answer(Response.json({ timeZone: 'Asia/Bishkek' }))
    expect(await screen.findByText('21:04:12')).toBeTruthy()
  })

  it('renders the same placeholder on the ssr server, so hydration matches', async () => {
    const { router, tree } = app(json({ timeZone: 'Asia/Bishkek' }))
    await router.load()
    const html = renderToString(tree)
    expect(html).toContain(CLOCK_PLACEHOLDER)
    expect(html).not.toContain('21:04:12')
  })

  it('falls back to utc, the server\'s own default, while the setting is unset', async () => {
    const { tree } = app(json({}))
    render(tree)
    expect(await screen.findByText('15:04:12')).toBeTruthy()
  })
})
