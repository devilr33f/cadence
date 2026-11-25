// @vitest-environment jsdom
import type { Detail, DeviceView } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiClient } from '@/api/client'
import { deviceQueryOptions } from '@/lib/devices'
import { INVALIDATE_WINDOW_MS } from '@/lib/live/invalidation'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { DevicesPage } from './devices-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

const KITCHEN = '0193a71e-55c0-7e2b-b1a4-6f0d93c2e7aa'
const BEDROOM = '0193a71e-55c0-7e2b-b1a4-6f0d93c2e7bb'
const HALL = '0193a71e-55c0-7e2b-b1a4-6f0d93c2e7cc'

const household: Record<string, FakeRoute> = {
  'GET /session': signedIn,
  'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
  'GET /boards': json([
    { id: 'sim', name: 'Simulator' },
  ]),
  'GET /devices': json([
    {
      id: KITCHEN,
      name: 'Kitchen',
      model: 'sim',
      room: 'Кухня',
      status: 'enrolled',
      online: true,
      lastSeenAt: '2026-11-12T15:04:12Z',
    },
    {
      id: BEDROOM,
      name: 'Bedroom',
      model: 'yandex-pickle',
      status: 'enrolled',
      online: false,
      lastSeenAt: '2026-11-11T09:30:00Z',
    },
    {
      id: HALL,
      name: 'Hall',
      model: 'sim',
      status: 'pending',
      online: false,
      lastSeenAt: null,
    },
  ]),
}

function renderDevices(routes: Record<string, FakeRoute> = household) {
  return renderApp({
    path: '/devices',
    routes,
    pages: { '/devices': DevicesPage },
  })
}

function rows(): string[][] {
  const table = screen.getByRole('table', { name: 'Devices' })
  return within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').map(cell => cell.textContent ?? ''))
}

describe('the devices list', () => {
  it('lists each device with its board, its state and when it was last seen, in the household\'s zone', async () => {
    renderDevices()
    expect(await screen.findByRole('link', { name: 'Kitchen' })).toBeTruthy()
    await waitFor(() => expect(rows()[0]?.[4]).toBe('12 Nov, 21:04'))
    expect(rows()).toEqual([
      [
        'Kitchen',
        'Online',
        'Кухня',
        'Simulator',
        '12 Nov, 21:04',
      ],
      [
        'Bedroom',
        'Offline',
        'None',
        'yandex-pickle',
        '11 Nov, 15:30',
      ],
      [
        'Hall',
        'Enrolling',
        'None',
        'Simulator',
        'Never',
      ],
    ])
    expect(screen.getByRole('link', { name: 'Kitchen' }).getAttribute('href')).toBe(`/devices/${KITCHEN}`)
  })

  it('takes the online flag from the list, fetching no detail', async () => {
    const { server } = renderDevices()
    await screen.findByRole('link', { name: 'Kitchen' })
    await waitFor(() => expect(rows()[0]?.[1]).toBe('Online'))
    expect(server.requests.some(r => new URL(r.url).pathname.startsWith('/api/v1/devices/'))).toBe(false)
  })

  it('sorts by state: online, offline, then enrolling', async () => {
    const user = setupUser()
    renderDevices({
      ...household,
      'GET /devices': json([
        {
          id: HALL,
          name: 'Hall',
          model: 'sim',
          status: 'pending',
          online: false,
          lastSeenAt: null,
        },
        {
          id: BEDROOM,
          name: 'Bedroom',
          model: 'sim',
          status: 'enrolled',
          online: false,
          lastSeenAt: null,
        },
        {
          id: KITCHEN,
          name: 'Kitchen',
          model: 'sim',
          status: 'enrolled',
          online: true,
          lastSeenAt: null,
        },
      ]),
    })
    await screen.findByRole('link', { name: 'Kitchen' })
    await user.click(screen.getByRole('button', { name: 'Status' }))
    expect(rows().map(r => r[0])).toEqual(['Kitchen', 'Bedroom', 'Hall'])
  })

  it('sorts by a column, and the other way on a second press', async () => {
    const user = setupUser()
    renderDevices()
    await screen.findByRole('link', { name: 'Kitchen' })
    await user.click(screen.getByRole('button', { name: 'Name' }))
    expect(rows().map(r => r[0])).toEqual(['Bedroom', 'Hall', 'Kitchen'])
    await user.click(screen.getByRole('button', { name: 'Name' }))
    expect(rows().map(r => r[0])).toEqual(['Kitchen', 'Hall', 'Bedroom'])
    await user.click(screen.getByRole('button', { name: 'Last seen' }))
    expect(rows().map(r => r[0])).toEqual(['Kitchen', 'Bedroom', 'Hall'])
  })

  it('follows the devices topic', async () => {
    renderDevices()
    await screen.findByRole('link', { name: 'Kitchen' })
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames()).toEqual([
      {
        subscribe: ['devices', 'presence'],
      },
    ]))
  })

  it('reads the list again on a device\'s events, not on a beat', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let online = false
    const { server } = renderDevices({
      ...household,
      'GET /devices': () => Response.json([
        {
          id: BEDROOM,
          name: 'Bedroom',
          model: 'yandex-pickle',
          status: 'enrolled',
          online,
          lastSeenAt: '2026-11-11T09:30:00Z',
        },
      ]),
    })
    await screen.findByRole('link', { name: 'Bedroom' })
    act(() => FakeSocket.last.open())
    await act(() => vi.advanceTimersByTimeAsync(35_000))
    expect(server.calls('GET /devices')).toBe(1)

    online = true
    act(() => FakeSocket.last.receive({
      type: 'device_online',
      deviceId: BEDROOM,
      at: '2026-11-12T15:05:00Z',
      data: { name: 'Bedroom', model: 'yandex-pickle', online: true },
    }))
    await waitFor(() => expect(rows()[0]?.[1]).toBe('Online'))
    expect(server.calls('GET /devices')).toBe(2)
  })

  it('drops a device deleted elsewhere, and what the cache held of it, without asking the server for it', async () => {
    const gone = new Set<string>()
    const listed = household['GET /devices']!
    const { server, queryClient } = renderDevices({
      ...household,
      'GET /devices': async (request) => {
        const devices = await (await listed(request)).json() as DeviceView[]
        return Response.json(devices.filter(d => !gone.has(d.id)))
      },
    })
    await screen.findByRole('link', { name: 'Kitchen' })
    // its page was open earlier
    queryClient.setQueryData(deviceQueryOptions(createApiClient(), KITCHEN).queryKey, {} as Detail)
    act(() => FakeSocket.last.open())
    gone.add(KITCHEN)
    act(() => FakeSocket.last.receive({
      type: 'device_deleted',
      deviceId: KITCHEN,
      at: '2026-11-12T15:05:00Z',
    }))
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Kitchen' })).toBeNull())
    expect(queryClient.getQueryCache().getAll().filter(q => JSON.stringify(q.queryKey).includes(KITCHEN))).toEqual([])
    await new Promise(resolve => setTimeout(resolve, INVALIDATE_WINDOW_MS * 2))
    expect(server.requests.filter(r => r.url.includes(KITCHEN))).toEqual([])
  })

  it('teaches what will be here, with the enroll action, when there is nothing yet', async () => {
    renderDevices({
      ...household,
      'GET /devices': json([]),
    })
    expect(await screen.findByText('No speakers yet')).toBeTruthy()
    const enroll = screen.getAllByRole('link', { name: 'Enroll a speaker' })
    expect(enroll.length).toBeGreaterThan(0)
    expect(enroll.every(link => link.getAttribute('href') === '/devices/enroll')).toBe(true)
  })

  it('says why the list could not be read, in codes, with a way to try again', async () => {
    renderDevices({
      ...household,
      'GET /devices': json({ error: 'forbidden' }, 403),
    })
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('The server answered 403 forbidden.')
    expect(within(alert).getByRole('button', { name: 'Try again' })).toBeTruthy()
  })
})
