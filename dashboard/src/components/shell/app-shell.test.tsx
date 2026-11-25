// @vitest-environment jsdom
import type { ComponentProps } from 'react'
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'

import { cleanup, render, screen, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { stubColorScheme } from '@/test/media'
import { setupUser } from '@/test/typing'
import { AppShell } from './app-shell'

beforeEach(() => {
  stubColorScheme(false)
  // the router resets the scroll on each navigation; jsdom has no layout to scroll
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})

afterEach(cleanup)

async function renderShell(badges?: ComponentProps<typeof AppShell>['badges']) {
  const root = createRootRoute({
    component: () => (
      <AppShell feed={{ state: 'idle' }} account={{ state: 'signed-out' }} badges={badges}>
        <Outlet />
      </AppShell>
    ),
  })
  const overview = createRoute({
    getParentRoute: () => root,
    path: '/',
    component: () => <h1>Overview</h1>,
  })
  const router = createRouter({
    routeTree: root.addChildren([overview]),
    history: createMemoryHistory({
      initialEntries: ['/'],
    }),
  })
  render(<RouterProvider router={router} />)
  await screen.findByRole('heading', { name: 'Overview' })
}

it('lists only the sections whose pages exist, the current one marked', async () => {
  await renderShell()
  const nav = screen.getByRole('navigation', { name: 'Main' })
  const overview = within(nav).getByRole('link', { name: /Overview/ })
  expect(overview.getAttribute('aria-current')).toBe('page')
  expect(within(nav).getByRole('link', { name: /Devices/ }).getAttribute('aria-current')).toBeNull()
  expect(within(nav).queryByRole('link', { name: /Metrics/ })).toBeNull()
  expect(within(nav).getAllByRole('link').map(link => link.textContent)).toEqual([
    'Overview',
    'Devices',
    'Groups',
    'Catalog',
    'History',
    'Providers',
    'Modes',
    'Skills',
    'Wake words',
    'Memory',
    'Tasks',
    'Integrations',
    'Logs',
    'Traces',
    'Settings',
    'Tokens',
  ])
  expect(within(nav).getByRole('link', { name: 'Traces' }).getAttribute('href')).toBe('/traces')
  expect(within(nav).getByRole('link', { name: 'Modes' }).getAttribute('href')).toBe('/modes')
  expect(within(nav).getByRole('link', { name: 'Skills' }).getAttribute('href')).toBe('/skills')
  expect(within(nav).getByRole('link', { name: 'Tokens' }).getAttribute('href')).toBe('/tokens')
})

it('shows a section\'s count beside it, worded for a screen reader', async () => {
  await renderShell({
    memory: { count: 3, label: '3 suggestions to review' },
  })
  const nav = screen.getByRole('navigation', { name: 'Main' })
  expect(within(nav).getByRole('link', { name: 'Memory, 3 suggestions to review' })).toBeTruthy()
  expect(within(nav).getByRole('link', { name: 'Tasks' }).textContent).toBe('Tasks')
})

it('shows a count that a full read capped as at least that many', async () => {
  await renderShell({
    memory: { count: 200, more: true, label: '200 or more suggestions to review' },
  })
  const nav = screen.getByRole('navigation', { name: 'Main' })
  const link = within(nav).getByRole('link', { name: 'Memory, 200 or more suggestions to review' })
  expect(link.textContent).toBe('Memory200+, 200 or more suggestions to review')
})

it('shows no count of nothing', async () => {
  await renderShell({
    memory: { count: 0, label: 'no suggestions' },
  })
  const nav = screen.getByRole('navigation', { name: 'Main' })
  expect(within(nav).getByRole('link', { name: 'Memory' }).textContent).toBe('Memory')
})

it('reaches every section by keyboard, in order', async () => {
  const user = setupUser()
  await renderShell()
  const nav = screen.getByRole('navigation', { name: 'Main' })
  const links = within(nav).getAllByRole('link')
  links[0]!.focus()
  for (const link of links.slice(1)) {
    await user.tab()
    expect(document.activeElement).toBe(link)
  }
})

it('shows the feed and the sign-in state in the header', async () => {
  await renderShell()
  const banner = screen.getByRole('banner')
  expect(within(banner).getByText('Not connected')).toBeTruthy()
  expect(within(banner).getByText('Not signed in')).toBeTruthy()
})

it('opens the navigation as a panel on a narrow screen', async () => {
  const user = setupUser()
  await renderShell()
  await user.click(screen.getByRole('button', { name: 'Open navigation' }))
  const panel = await screen.findByRole('dialog', { name: 'Navigation' })
  expect(within(panel).getByRole('link', { name: /Overview/ })).toBeTruthy()
})

it('has no accessibility violations axe can see without layout', async () => {
  await renderShell()
  const results = await axe.run(document.body, {
    rules: {
      'color-contrast': { enabled: false },
    },
  })
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})
