// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { KITCHEN, queryOf } from '@/test/household'
import { CONNECTED, entitiesRoute, integrationRoutes, NOT_CONFIGURED } from '@/test/integrations'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { IntegrationsPage } from './integrations-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function renderIntegrations(routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: '/integrations',
    routes: integrationRoutes(routes),
    pages: { '/integrations': IntegrationsPage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

function region(name: string) {
  return screen.getByRole('region', { name })
}

function rows(name: string, columns: number): string[][] {
  const table = screen.getByRole('table', { name })
  return within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').slice(0, columns).map(cell => cell.textContent ?? ''))
}

describe('home assistant', () => {
  it('shows a connected client: its state, its areas, its entities with their states as text, and what is used most', async () => {
    renderIntegrations()
    await within(await screen.findByRole('region', { name: 'Home Assistant' })).findByText('Connected')
    const ha = region('Home Assistant')
    expect(within(ha).getAllByText('home', { selector: 'dd *, dd' }).length).toBeGreaterThan(0)
    expect(within(ha).getByText('3 entities in 2 areas')).toBeTruthy()
    await waitFor(() => expect(rows('Entities', 5)).toHaveLength(3))
    expect(rows('Areas', 3)).toEqual([
      ['Kitchen', 'kitchen', 'cooking'],
      ['Living room', 'living_room', '—'],
    ])
    expect(rows('Entities', 5)).toEqual([
      [
        'Kitchen light',
        'light.kitchen',
        'light',
        'Kitchen',
        'on',
      ],
      [
        'Kettle',
        'switch.kettle',
        'switch',
        'Kitchen',
        'off',
      ],
      [
        'Big lamp',
        'light.living_room',
        'light',
        'Living room',
        '(text)',
      ],
    ])
    expect(rows('Most used entities', 2)).toEqual([
      ['light.kitchen', '12.5'],
      ['switch.kettle', '3'],
    ])
  })

  it('points at the provider and the setting when it is not set up, and reads nothing it cannot', async () => {
    const { server } = renderIntegrations({
      'GET /integrations/homeassistant': json(NOT_CONFIGURED),
    })
    expect(await screen.findByText('Home Assistant is not set up')).toBeTruthy()
    const ha = region('Home Assistant')
    expect(within(ha).getByRole('link', { name: 'Add a provider' }).getAttribute('href')).toBe('/providers')
    expect(within(ha).getByRole('link', { name: 'Open the homeAssistant setting' }).getAttribute('href')).toBe('/settings#setting-homeAssistant')
    expect(server.calls('GET /integrations/homeassistant/entities')).toBe(0)
    expect(server.calls('GET /integrations/homeassistant/areas')).toBe(0)
    expect(server.calls('GET /integrations/homeassistant/usage')).toBe(0)
  })

  it.each([
    [
      {
        ...CONNECTED,
        connected: false,
        lastError: 'auth_failed',
        restartRequired: true,
      },
      'Restart required',
      'auth_failed',
    ],
    [
      { ...CONNECTED, connected: false, lastError: 'connect_failed' },
      'Failed',
      'connect_failed',
    ],
    [
      { ...CONNECTED, connected: false },
      'Connecting',
      undefined,
    ],
  ])('words a client that is not connected: %j', async (status, label, code) => {
    renderIntegrations({
      'GET /integrations/homeassistant': json(status),
    })
    const ha = await screen.findByRole('region', { name: 'Home Assistant' })
    expect(await within(ha).findByText(label)).toBeTruthy()
    if (code)
      expect(within(ha).getAllByText(code).length).toBeGreaterThan(0)
  })

  it('names the version Home Assistant reported, and none before it authenticated', async () => {
    renderIntegrations()
    await within(await screen.findByRole('region', { name: 'Home Assistant' })).findByText('Connected')
    expect(within(region('Home Assistant')).getByText('Version').nextElementSibling?.textContent).toBe('2026.11.1')
    cleanup()
    renderIntegrations({
      'GET /integrations/homeassistant': json({ ...CONNECTED, connected: false, version: null }),
    })
    await screen.findByText('Connecting')
    expect(within(region('Home Assistant')).queryByText('Version')).toBeNull()
  })

  it('says why the status could not be read, in codes', async () => {
    renderIntegrations({
      'GET /integrations/homeassistant': json({ error: 'forbidden' }, 403),
    })
    const ha = await screen.findByRole('region', { name: 'Home Assistant' })
    expect(await within(ha).findByText('The server answered 403 forbidden.')).toBeTruthy()
  })

  it('narrows the entities by a search, an area and a domain, on the server', async () => {
    const { server, user } = renderIntegrations()
    await waitFor(() => expect(rows('Entities', 1)).toHaveLength(3))
    const ha = region('Home Assistant')
    await user.type(within(ha).getByRole('searchbox', { name: 'Search entities' }), 'lamp')
    await waitFor(() => expect(rows('Entities', 1)).toEqual([
      ['Big lamp'],
    ]))
    expect(queryOf(server.requests.filter(r => r.url.includes('/entities')).at(-1)!).q).toBe('lamp')
    await user.clear(within(ha).getByRole('searchbox', { name: 'Search entities' }))
    await user.selectOptions(within(ha).getByRole('combobox', { name: 'Area' }), 'Kitchen')
    await waitFor(() => expect(rows('Entities', 1)).toEqual([
      ['Kitchen light'],
      ['Kettle'],
    ]))
    expect(queryOf(server.requests.filter(r => r.url.includes('/entities')).at(-1)!)).toMatchObject({ area: 'kitchen' })
    await user.selectOptions(within(ha).getByRole('combobox', { name: 'Domain' }), 'switch')
    await waitFor(() => expect(rows('Entities', 1)).toEqual([
      ['Kettle'],
    ]))
    expect(queryOf(server.requests.filter(r => r.url.includes('/entities')).at(-1)!)).toMatchObject({ area: 'kitchen', domain: 'switch' })
    expect(rows('Entities', 1)).toEqual([
      ['Kettle'],
    ])
  })

  it('finds a domain beyond the first 500 entities, offering every domain Home Assistant has', async () => {
    // a large home: 600 sensors sort before the one weather entity, so the first read of 500 holds no weather
    const sensors = Array.from({ length: 600 }, (_, i) => ({
      id: `sensor.probe_${String(i).padStart(3, '0')}`,
      name: `Probe ${i}`,
      domain: 'sensor',
      area: '',
      areaName: '',
      state: '20',
    }))
    const weather = {
      id: 'weather.home',
      name: 'Forecast',
      domain: 'weather',
      area: '',
      areaName: '',
      state: 'sunny',
    }
    const { server, user } = renderIntegrations({
      'GET /integrations/homeassistant/entities': entitiesRoute([...sensors, weather]),
    })
    await waitFor(() => expect(rows('Entities', 1)).toHaveLength(500))
    expect(screen.getByText('The first 500 are shown: search, or choose an area or a domain, to narrow them.')).toBeTruthy()
    await user.selectOptions(within(region('Home Assistant')).getByRole('combobox', { name: 'Domain' }), 'weather')
    await waitFor(() => expect(rows('Entities', 5)).toEqual([
      [
        'Forecast',
        'weather.home',
        'weather',
        '—',
        'sunny',
      ],
    ]))
    expect(queryOf(server.requests.filter(r => r.url.includes('/entities')).at(-1)!)).toMatchObject({ domain: 'weather', limit: '500' })
  })

  it('shows one speaker\'s most used entities on request', async () => {
    const { server, user } = renderIntegrations()
    await waitFor(() => expect(rows('Most used entities', 1)).toHaveLength(2))
    await user.selectOptions(within(region('Home Assistant')).getByRole('combobox', { name: 'Used by' }), 'Kitchen')
    await waitFor(() => expect(rows('Most used entities', 2)).toEqual([
      ['light.living_room', '2'],
    ]))
    expect(queryOf(server.requests.filter(r => r.url.includes('/usage')).at(-1)!).device).toBe(KITCHEN)
  })
})

describe('hermes', () => {
  it('says what runs the tasks, and links its setting', async () => {
    renderIntegrations()
    const hermes = await screen.findByRole('region', { name: 'Hermes' })
    expect(await within(hermes).findByText('Running')).toBeTruthy()
    expect(within(hermes).getByText(/Tasks run on/).textContent).toBe('Tasks run on hermes.')
    expect(within(hermes).getByRole('link', { name: 'Open the hermes setting' }).getAttribute('href')).toBe('/settings#setting-hermes')
  })

  it.each([
    [
      [
        { name: 'hermes', instance: null, configured: 'hermes' },
      ],
      'Restart required',
    ],
    [
      [
        { name: 'hermes', instance: 'old', configured: 'hermes' },
      ],
      'Restart required',
    ],
    [
      [
        { name: 'hermes', instance: null, configured: null },
      ],
      'Not set up',
    ],
    [[], 'Not set up'],
  ])('words %j', async (kinds, label) => {
    renderIntegrations({
      'GET /tasks/kinds': json(kinds),
    })
    const hermes = await screen.findByRole('region', { name: 'Hermes' })
    expect(await within(hermes).findByText(label)).toBeTruthy()
  })
})

it('has no accessibility violations axe can see without layout', async () => {
  renderIntegrations()
  await waitFor(() => expect(rows('Entities', 1)).toHaveLength(3))
  await screen.findByRole('table', { name: 'Tools of notes' })
  // jsdom has no canvas; axe only probes for one
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  const results = await axe.run(document.body, {
    rules: {
      'color-contrast': { enabled: false },
    },
  })
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})
