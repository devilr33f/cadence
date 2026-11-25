// @vitest-environment jsdom
import type { BusyView } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { KITCHEN } from '@/test/household'
import { integrationRoutes } from '@/test/integrations'
import { stubColorScheme } from '@/test/media'
import { DND, NOT_BUSY } from '@/test/subscriptions'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { IntegrationsPage } from '../integrations-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const PUT = 'PUT /busy/dashboard'
const DELETE = 'DELETE /busy/dashboard'

// the server's busy state, which the test's put and delete change
function busyStore(initial: BusyView) {
  const state = { view: initial }
  const routes: Record<string, FakeRoute> = {
    'GET /busy': () => Response.json(state.view),
    [PUT]: async (request) => {
      const body = await request.clone().json() as { ttlSeconds: number }
      const signal = {
        source: 'dashboard',
        reason: 'dnd' as const,
        devices: null,
        expiresAt: new Date(Date.parse('2026-11-14T12:00:00Z') + body.ttlSeconds * 1000).toISOString(),
      }
      state.view = {
        signals: [signal],
        devices: [
          { deviceId: KITCHEN, reason: 'dnd' },
        ],
      }
      return Response.json(signal)
    },
    [DELETE]: () => {
      state.view = NOT_BUSY
      return new Response(null, { status: 204 })
    },
  }
  return { state, routes }
}

function renderBusy(routes: Record<string, FakeRoute> = {}) {
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

function bodiesOf(server: ReturnType<typeof renderApp>['server'], key: string) {
  return Promise.all(server.requests
    .filter(r => `${r.method} ${new URL(r.url).pathname.replace(/^\/api\/v1/, '')}` === key)
    .map(r => r.clone().json() as Promise<unknown>))
}

async function busySection() {
  return screen.findByRole('region', { name: 'Busy' })
}

describe('busy mode on the integrations page', () => {
  it('turns do-not-disturb on for the chosen time on every speaker, then off, the focus following the control', async () => {
    const store = busyStore(NOT_BUSY)
    const { server, user } = renderBusy(store.routes)
    const region = await busySection()
    await within(region).findByText('No one is busy')
    await user.selectOptions(within(region).getByRole('combobox', { name: 'Do not disturb for' }), '2 hours')
    await user.click(within(region).getByRole('button', { name: 'Turn on do not disturb' }))
    const end = await within(region).findByRole('button', { name: 'End do not disturb' })
    await expectFocusOn(end)
    expect(await bodiesOf(server, PUT)).toEqual([
      { reason: 'dnd', devices: null, ttlSeconds: 7200 },
    ])
    expect(within(region).getByRole('status').textContent).toContain('Do not disturb')

    await user.click(end)
    const select = await within(region).findByRole('combobox', { name: 'Do not disturb for' })
    await expectFocusOn(select)
    expect(server.calls(DELETE)).toBe(1)
  })

  it('words a refusal by its code beside the control', async () => {
    const { user } = renderBusy({
      [PUT]: json({ error: 'invalid', path: '/body/ttlSeconds' }, 422),
    })
    const region = await busySection()
    const button = await within(region).findByRole('button', { name: 'Turn on do not disturb' })
    await user.click(button)
    expect((await within(region).findByRole('alert')).textContent).toBe('The server answered 422 invalid.')
    expect(document.activeElement).toBe(button)
  })

  it('lists the signals in force: each source, reason and end; home assistant\'s cannot be cleared here', async () => {
    const { server, user } = renderBusy({
      'GET /busy': json(DND),
      [DELETE]: () => new Response(null, { status: 204 }),
    })
    const region = await busySection()
    const table = await within(region).findByRole('table', { name: 'Busy signals' })
    await within(table).findByText('ha:binary_sensor.mac_audio_input_in_use')
    const cells = within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').slice(0, 4).map(cell => cell.textContent))
    expect(cells).toEqual([
      [
        'Dashboarddashboard',
        'Do not disturb',
        'Every speaker',
        expect.stringMatching(/14 Nov/),
      ],
      [
        'Home Assistantha:binary_sensor.mac_audio_input_in_use',
        'In a meeting',
        'Every speaker',
        'While the state holds',
      ],
    ])
    expect(within(table).queryByRole('button', { name: /^Clear: ha:/ })).toBeNull()
    await user.click(within(table).getByRole('button', { name: 'Clear: dashboard' }))
    await waitFor(() => expect(server.calls(DELETE)).toBe(1))
  })

  it('takes a signal already gone as cleared', async () => {
    const { user } = renderBusy({
      'GET /busy': json(DND),
      [DELETE]: json({ error: 'not_found' }, 404),
    })
    const region = await busySection()
    await user.click(await within(region).findByRole('button', { name: 'End do not disturb' }))
    await waitFor(() => expect(within(region).queryByRole('alert')).toBeNull())
  })
})

describe('the busy menu in the header', () => {
  it('says no speaker is busy, and sets do-not-disturb for a time from its menu', async () => {
    const store = busyStore(NOT_BUSY)
    const { server, user } = renderBusy(store.routes)
    const trigger = await screen.findByRole('button', { name: 'Busy mode: no speaker is busy' })
    await user.click(trigger)
    const menu = await screen.findByRole('menu')
    expect(within(menu).getByText('No speaker is busy')).toBeTruthy()
    await user.click(within(menu).getByRole('menuitem', { name: 'Do not disturb for 1 hour' }))
    await screen.findByRole('button', { name: 'Busy mode: do not disturb, 1 speaker holding updates' })
    expect(await bodiesOf(server, PUT)).toEqual([
      { reason: 'dnd', devices: null, ttlSeconds: 3600 },
    ])
  })

  it('names the busy speakers and why, and ends do-not-disturb', async () => {
    const { server, user } = renderBusy({
      'GET /busy': json(DND),
      [DELETE]: () => new Response(null, { status: 204 }),
    })
    await user.click(await screen.findByRole('button', { name: 'Busy mode: do not disturb, 2 speakers holding updates' }))
    const menu = await screen.findByRole('menu')
    expect(within(menu).getByText('Kitchen')).toBeTruthy()
    expect(within(menu).getByText('Bedroom')).toBeTruthy()
    expect(within(menu).getAllByText('In a meeting')).toHaveLength(2)
    await user.click(within(menu).getByRole('menuitem', { name: 'End do not disturb' }))
    await waitFor(() => expect(server.calls(DELETE)).toBe(1))
  })

  it('reads the busy state again on the presence topic', async () => {
    const store = busyStore(NOT_BUSY)
    const { server } = renderBusy(store.routes)
    await screen.findByRole('button', { name: 'Busy mode: no speaker is busy' })
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
      subscribe: ['agent', 'presence', 'subscriptions'],
    }))
    const before = server.calls('GET /busy')
    store.state.view = {
      signals: [
        {
          source: 'voice',
          reason: 'dnd',
          devices: null,
          expiresAt: '2026-11-14T13:00:00Z',
        },
      ],
      devices: [
        { deviceId: KITCHEN, reason: 'dnd' },
      ],
    }
    act(() => FakeSocket.last.receive({
      type: 'busy_changed',
      deviceId: KITCHEN,
      data: { busy: true, reason: 'dnd' },
    }))
    await waitFor(() => expect(server.calls('GET /busy')).toBeGreaterThan(before))
    expect(await screen.findByRole('button', { name: 'Busy mode: 1 speaker busy, holding updates' })).toBeTruthy()
  })
})
