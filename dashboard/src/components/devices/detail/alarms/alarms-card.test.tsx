import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ALARMS, detail, devicePage, ID } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
// @vitest-environment jsdom
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

const CAP = `/devices/${ID}/capabilities`

const STATE = {
  alarms: [
    {
      id: 'a1',
      time: '07:30',
      days: ['mon', 'tue', 'wed', 'thu', 'fri'],
      level: 40,
      next: '2026-11-13T07:39:00Z',
    },
  ],
  timers: [],
  reminders: [
    {
      id: 'r1',
      time: '18:00',
      date: '2026-11-12',
      label: 'Take the bins out',
      next: '2026-11-12T18:00:00Z',
    },
  ],
  ringing: { kind: 'alarm', id: 'a1' },
  snoozed: [],
  lastRing: {
    kind: 'timer',
    id: 't1',
    at: '2026-11-12T10:00:00Z',
    stoppedBy: 'button',
  },
}

async function renderAlarms(overrides: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: detail({
        capabilities: [
          { ...ALARMS, state: STATE },
        ],
      }),
    }, overrides),
    pages: { '/devices/$id': DevicePage },
  })
  await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
  return {
    ...app,
    user: setupUser(),
    card: screen.getByRole('region', { name: 'alarms' }),
  }
}

it('summarizes the next ring and opens the full tab, without a set or cancel command', async () => {
  const { card, user } = await renderAlarms()
  expect(within(card).getByText('Next: Reminder')).toBeTruthy()
  expect(within(card).queryByRole('heading', { level: 4 })).toBeNull()
  expect(within(card).queryByRole('button', { name: /set_|cancel_/ })).toBeNull()

  await user.click(within(card).getByRole('button', { name: 'Open Alarms' }))
  expect(await screen.findByRole('tab', { name: 'Alarms', selected: true })).toBeTruthy()
  expect(await screen.findByRole('heading', { level: 2, name: 'Alarms' })).toBeTruthy()
  expect(screen.getByText('Weekdays')).toBeTruthy()
  expect(screen.getByText('Level 40')).toBeTruthy()
  expect(screen.getAllByText('Take the bins out').length).toBeGreaterThan(0)
})

it('stops a ring with the device\'s stop_ring, and says a refusal of snooze by its code', async () => {
  const { server, user, card } = await renderAlarms({
    [`POST ${CAP}/alarms/commands/stop_ring`]: json({ result: null }),
    [`POST ${CAP}/alarms/commands/snooze`]: json({ error: 'command_failed', detail: 'not_ringing' }, 409),
  })
  expect(within(card).getByText('Ringing')).toBeTruthy()
  await user.click(within(card).getByRole('button', { name: 'Stop' }))
  await waitFor(() => expect(server.calls(`POST ${CAP}/alarms/commands/stop_ring`)).toBe(1))
  const stop = server.requests.find(r => r.method === 'POST')!
  expect(await stop.clone().json()).toEqual({ kind: 'alarm', id: 'a1' })

  await user.click(within(card).getByRole('button', { name: 'Snooze' }))
  expect((await within(card).findByRole('alert')).textContent)
    .toBe('Snooze failed. The server answered 409 command_failed (not_ringing).')
  const snooze = server.requests.find(r => new URL(r.url).pathname.endsWith('/snooze'))!
  expect(await snooze.clone().json()).toEqual({ kind: 'alarm', id: 'a1' })
})
