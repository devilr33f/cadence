// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ALARMS, detail, devicePage, ID } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { DevicePage } from '../device-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const CAP = `/devices/${ID}/capabilities/alarms/commands`
const STATE = {
  alarms: [
    {
      id: 'a1',
      time: '07:30',
      days: ['mon', 'tue', 'wed', 'thu', 'fri'],
      level: 40,
      rampSeconds: 120,
      rampFrom: 'quiet',
      limitSeconds: 900,
      next: '2026-11-13T07:39:00Z',
    },
  ],
  timers: [
    {
      id: 'tea',
      seconds: 600,
      endsAt: new Date(Date.now() + 600_000).toISOString(),
    },
  ],
  reminders: [
    {
      id: 'bins',
      time: '18:00',
      days: ['mon', 'thu'],
      label: 'Take the bins out',
      next: '2026-11-12T18:00:00Z',
    },
  ],
  ringing: null,
  snoozed: [],
  lastRing: null,
}

async function renderTab({ state = STATE, globalSettings = { timeZone: 'Asia/Bishkek' }, deviceSettings = {}, overrides = {} }: {
  state?: unknown
  globalSettings?: object
  deviceSettings?: object
  overrides?: Record<string, FakeRoute>
} = {}) {
  const app = renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: detail({
        capabilities: [
          { ...ALARMS, state },
        ],
      }),
    }, {
      'GET /settings/global': json(globalSettings),
      [`GET /devices/${ID}/settings`]: json(deviceSettings),
      ...overrides,
    }),
    pages: { '/devices/$id': DevicePage },
  })
  await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
  const user = setupUser()
  await user.click(screen.getByRole('tab', { name: 'Alarms' }))
  await screen.findByRole('heading', { level: 2, name: 'Alarms' })
  return { ...app, user }
}

describe('the alarms tab', () => {
  it('shows alarms read-only, with their ramp and limit, and no alarm commands', async () => {
    await renderTab()
    const alarms = screen.getByRole('region', { name: 'Alarms' })
    expect(within(alarms).getByText('Weekdays')).toBeTruthy()
    expect(within(alarms).getByText('Level 40')).toBeTruthy()
    expect(within(alarms).getByText('Ramp 120 s from quiet')).toBeTruthy()
    expect(within(alarms).getByText('Ring limit 900 s')).toBeTruthy()
    expect(within(alarms).queryAllByRole('button')).toEqual([])
    expect(screen.queryByRole('button', { name: /set_alarm|cancel_alarm/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Cancel timer tea' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Cancel reminder: Take the bins out' })).toBeTruthy()
    const countdown = within(screen.getByRole('region', { name: 'Timers' })).getByLabelText(/\d+ seconds left/)
    const left = Number(countdown.getAttribute('aria-label')!.split(' ')[0])
    expect(left).toBeGreaterThan(0)
    expect(left).toBeLessThanOrEqual(601)
  })

  it('is not offered without the alarms capability', async () => {
    renderApp({
      path: `/devices/${ID}`,
      routes: devicePage({
        detail: detail({
          capabilities: detail().capabilities!.filter(capability => capability.id !== 'alarms'),
        }),
      }),
      pages: { '/devices/$id': DevicePage },
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    expect(screen.queryByRole('tab', { name: 'Alarms' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'alarms' })).toBeNull()
  })

  it('validates and sets a timer with the device\'s id and total seconds', async () => {
    const { server, user } = await renderTab({
      overrides: {
        [`POST ${CAP}/set_timer`]: json({ result: null }),
      },
    })
    await user.click(screen.getByRole('button', { name: 'Add timer' }))
    const dialog = await screen.findByRole('dialog', { name: 'Set a timer' })
    await user.click(within(dialog).getByRole('button', { name: 'Set timer' }))
    expect(await within(dialog).findByRole('alert')).toHaveProperty('textContent', 'Use 1–16 lowercase letters or digits, with no spaces.')
    expect(server.calls(`POST ${CAP}/set_timer`)).toBe(0)
    expect(document.activeElement).toBe(within(dialog).getByRole('textbox', { name: 'Timer ID (required)' }))

    await user.type(within(dialog).getByRole('textbox', { name: 'Timer ID (required)' }), 'tea')
    for (const [label, value] of [
      ['Hours', '1'],
      ['Minutes', '1'],
      ['Seconds', '1'],
    ] as const) {
      const field = within(dialog).getByLabelText(label)
      await user.clear(field)
      await user.type(field, value)
    }
    await user.click(within(dialog).getByRole('button', { name: 'Set timer' }))
    await waitFor(() => expect(server.calls(`POST ${CAP}/set_timer`)).toBe(1))
    const request = server.requests.find(item => new URL(item.url).pathname.endsWith('/set_timer'))!
    expect(await request.clone().json()).toEqual({ id: 'tea', seconds: 3661 })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('sets a weekday reminder without a session or a level', async () => {
    const { server, user } = await renderTab({
      overrides: {
        [`POST ${CAP}/set_reminder`]: json({ result: null }),
      },
    })
    await user.click(screen.getByRole('button', { name: 'Add reminder' }))
    const dialog = await screen.findByRole('dialog', { name: 'Set a reminder' })
    await user.type(within(dialog).getByRole('textbox', { name: 'Reminder ID (required)' }), 'plants')
    await user.type(within(dialog).getByRole('textbox', { name: 'Text (required)' }), 'Water the plants')
    await user.click(within(dialog).getByRole('radio', { name: 'At a time' }))
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Repeats (required)' }), 'days')
    await user.click(within(dialog).getByRole('checkbox', { name: 'Monday' }))
    await user.click(within(dialog).getByRole('checkbox', { name: 'Wednesday' }))
    await user.click(within(dialog).getByRole('button', { name: 'Set reminder' }))

    await waitFor(() => expect(server.calls(`POST ${CAP}/set_reminder`)).toBe(1))
    const request = server.requests.find(item => new URL(item.url).pathname.endsWith('/set_reminder'))!
    expect(await request.clone().json()).toEqual({
      id: 'plants',
      text: 'Water the plants',
      time: '18:00',
      days: ['mon', 'wed'],
    })
  })

  it('asks before cancelling a timer and a reminder, then sends only the id', async () => {
    const { server, user } = await renderTab({
      overrides: {
        [`POST ${CAP}/cancel_timer`]: json({
          result: {
            cancelled: ['tea'],
          },
        }),
        [`POST ${CAP}/cancel_reminder`]: json({
          result: {
            cancelled: ['bins'],
          },
        }),
      },
    })
    await user.click(screen.getByRole('button', { name: 'Cancel timer tea' }))
    let dialog = await screen.findByRole('alertdialog')
    expect(server.calls(`POST ${CAP}/cancel_timer`)).toBe(0)
    await user.click(within(dialog).getByRole('button', { name: 'Cancel timer' }))
    await waitFor(() => expect(server.calls(`POST ${CAP}/cancel_timer`)).toBe(1))
    expect(await server.requests.find(item => new URL(item.url).pathname.endsWith('/cancel_timer'))!.clone().json()).toEqual({ id: 'tea' })
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { level: 2, name: 'Timers' })))

    await user.click(screen.getByRole('button', { name: 'Cancel reminder: Take the bins out' }))
    dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText('Take the bins out')).toBeTruthy()
    expect(server.calls(`POST ${CAP}/cancel_reminder`)).toBe(0)
    await user.click(within(dialog).getByRole('button', { name: 'Cancel reminder' }))
    await waitFor(() => expect(server.calls(`POST ${CAP}/cancel_reminder`)).toBe(1))
    expect(await server.requests.find(item => new URL(item.url).pathname.endsWith('/cancel_reminder'))!.clone().json()).toEqual({ id: 'bins' })
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { level: 2, name: 'Reminders' })))
  })

  it('warns when no scope explicitly sets the time zone', async () => {
    await renderTab({ globalSettings: {} })
    expect(await screen.findByText('Time zone is not set')).toBeTruthy()
    const row = await screen.findByRole('row', { name: 'Time zone' })
    expect(within(row).getAllByRole('cell').slice(0, 3).map(cell => cell.textContent)).toEqual([
      'Time zonetimeZone',
      '—',
      'Server default',
    ])
  })

  it('shows inherited and overridden alarm settings and edits the device override', async () => {
    const { server, user } = await renderTab({
      globalSettings: {
        timeZone: 'Asia/Bishkek',
        alarms: { snoozeMinutes: 10 },
      },
      deviceSettings: {
        alarms: { snoozeMinutes: 9 },
      },
      overrides: {
        [`PUT /devices/${ID}/settings`]: json({
          alarms: { snoozeMinutes: 12 },
        }),
      },
    })
    expect(screen.queryByText('Time zone is not set')).toBeNull()
    const timeZone = await screen.findByRole('row', { name: 'Time zone' })
    expect(within(timeZone).getAllByRole('cell').slice(0, 3).map(cell => cell.textContent)).toEqual([
      'Time zonetimeZone',
      'Asia/Bishkek',
      'Inherited',
    ])
    const alarms = screen.getByRole('row', { name: 'Alarms and timers' })
    expect(within(alarms).getAllByRole('cell')[2]?.textContent).toBe('Overridden')
    await user.click(within(alarms).getByRole('button', { name: 'Edit Alarms and timers' }))
    const field = await screen.findByRole('textbox', { name: 'snoozeMinutes' })
    await user.clear(field)
    await user.type(field, '12')
    await user.click(screen.getByRole('button', { name: 'Save the override' }))
    await waitFor(() => expect(server.calls(`PUT /devices/${ID}/settings`)).toBe(1))
    expect(await server.requests.find(item => item.method === 'PUT')!.clone().json()).toEqual({
      alarms: { snoozeMinutes: 12 },
    })
  })

  it('has no accessibility violations axe can see without layout', async () => {
    await renderTab()
    // the overview stays mounted behind the tab and its media card may probe a canvas
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
    const results = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
      },
    })
    expect(results.violations.map(violation => `${violation.id}: ${violation.nodes.map(node => node.target.join(' ')).join(', ')}`)).toEqual([])
  })
})
