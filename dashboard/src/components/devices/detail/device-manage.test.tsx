import type { UserEvent } from '@testing-library/user-event'
// @vitest-environment jsdom
import type { Detail } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiClient } from '@/api/client'
import { devicesQueryOptions } from '@/lib/devices'
import { logsQueryOptions } from '@/lib/logs'
import { ALARMS, detail, devicePage, ID } from '@/test/device-detail'
import { json, noContent, TEST_BASE_URL } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { METRICS, metricsRoute, PROCESSES } from '@/test/health'
import { DEVICES } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { replace, setupUser } from '@/test/typing'
import { DevicePage } from './device-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

const RENAME = `PATCH /devices/${ID}`
const DELETE = `DELETE /devices/${ID}`

function renderDevice(overrides: Record<string, FakeRoute> = {}, current: { detail: Detail } = {
  detail: detail(),
}) {
  const app = renderApp({
    path: `/devices/${ID}`,
    routes: devicePage(current, overrides),
    pages: { '/devices/$id': DevicePage },
  })
  // the list the overview and the devices page read, this device in it
  app.queryClient.setQueryData(devicesQueryOptions(createApiClient()).queryKey, [
    { ...DEVICES[0]!, id: ID },
    DEVICES[1]!,
  ])
  return app
}

async function startRename(user: UserEvent) {
  await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
  await user.click(screen.getByRole('button', { name: 'Rename Kitchen' }))
  return screen.getByRole('textbox', { name: 'Name' }) as HTMLInputElement
}

function listInvalidated(queryClient: ReturnType<typeof renderDevice>['queryClient']) {
  return queryClient.getQueryState(devicesQueryOptions(createApiClient()).queryKey)?.isInvalidated
}

describe('renaming a device', () => {
  it('renames it in the header, trimmed, and marks the list stale', async () => {
    const user = setupUser()
    const current = {
      detail: detail(),
    }
    const { server, queryClient } = renderDevice({
      [RENAME]: async (request) => {
        const { name } = await request.clone().json() as { name: string }
        current.detail = detail({ name })
        return Response.json(current.detail)
      },
    }, current)
    const field = await startRename(user)
    await expectFocusOn(field)
    expect(field.value).toBe('Kitchen')

    await user.clear(field)
    await user.type(field, '  Pantry  ')
    await user.click(screen.getByRole('button', { name: 'Save the name' }))

    expect(await screen.findByRole('heading', { level: 1, name: 'Pantry' })).toBeTruthy()
    const patch = server.requests.find(r => r.method === 'PATCH')!
    expect(await patch.clone().json()).toEqual({ name: 'Pantry' })
    expect(screen.queryByRole('textbox', { name: 'Name' })).toBeNull()
    await expectFocusOn(screen.getByRole('button', { name: 'Rename Pantry' }))
    expect(listInvalidated(queryClient)).toBe(true)
  })

  it.each([
    ['   ', 'Give the speaker a name.'],
    [
      'ё'.repeat(65),
      'At most 64 characters.',
    ],
    ['kitchen​', 'No control or format characters.'],
  ])('refuses %j before sending it', async (typed, message) => {
    const user = setupUser()
    const { server } = renderDevice()
    const field = await startRename(user)
    await replace(user, field, typed)
    await user.click(screen.getByRole('button', { name: 'Save the name' }))
    expect((await screen.findByRole('alert')).textContent).toBe(message)
    expect(field.getAttribute('aria-invalid')).toBe('true')
    await expectFocusOn(field)
    expect(server.calls(RENAME)).toBe(0)
  })

  it.each([
    [409, 'name_taken', 'Another speaker has this name, in any case (name_taken).'],
    [422, 'invalid', 'The server refused this name (invalid).'],
  ])('places a %i %s on the field', async (status, error, message) => {
    const user = setupUser()
    renderDevice({
      [RENAME]: json({
        error,
        ...(status === 422 && { path: '/body/name' }),
      }, status),
    })
    const field = await startRename(user)
    await user.clear(field)
    await user.type(field, 'Bedroom')
    await user.click(screen.getByRole('button', { name: 'Save the name' }))
    expect((await screen.findByRole('alert')).textContent).toBe(message)
    expect(field.getAttribute('aria-invalid')).toBe('true')
    await expectFocusOn(field)
    expect(screen.getByRole('heading', { level: 1, name: 'Kitchen' })).toBeTruthy()
  })

  it('words any other refusal beside the button, which keeps the focus, and reads the device again', async () => {
    const user = setupUser()
    const { server } = renderDevice({
      [RENAME]: json({ error: 'internal' }, 500),
    })
    const field = await startRename(user)
    await user.clear(field)
    await user.type(field, 'Pantry')
    const save = screen.getByRole('button', { name: 'Save the name' })
    await user.click(save)
    expect((await screen.findByRole('alert')).textContent).toBe('The server answered 500 internal.')
    expect(field.getAttribute('aria-invalid')).toBeNull()
    await expectFocusOn(save)
    // a rename whose answer failed may still have landed
    await waitFor(() => expect(server.calls(`GET /devices/${ID}`)).toBe(2))
  })

  it('ignores a second press while the rename is on its way', async () => {
    const user = setupUser()
    const answers: ((response: Response) => void)[] = []
    renderDevice({
      [RENAME]: () => new Promise<Response>((resolve) => {
        answers.push(resolve)
      }),
    })
    const field = await startRename(user)
    await user.clear(field)
    await user.type(field, 'Pantry')
    const save = screen.getByRole('button', { name: 'Save the name' })
    await user.click(save)
    await waitFor(() => expect(answers).toHaveLength(1))
    expect(save.getAttribute('aria-disabled')).toBe('true')
    await user.click(save)
    await user.keyboard('{Enter}')
    expect(answers).toHaveLength(1)
    answers[0]!(Response.json(detail({ name: 'Pantry' })))
    expect(await screen.findByRole('heading', { level: 1, name: 'Pantry' })).toBeTruthy()
  })

  it('closes without a request on Cancel, on Escape and for the same name, the focus back on Rename', async () => {
    const user = setupUser()
    const { server } = renderDevice()
    const rename = () => screen.getByRole('button', { name: 'Rename Kitchen' })
    await startRename(user)
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await expectFocusOn(rename())

    await user.type(await startRename(user), 'x{Escape}')
    await expectFocusOn(rename())

    await user.type(await startRename(user), '  ')
    await user.click(screen.getByRole('button', { name: 'Save the name' }))
    await expectFocusOn(rename())
    expect(server.calls(RENAME)).toBe(0)
  })
})

describe('deleting a device', () => {
  async function openDelete(user: UserEvent) {
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const zone = screen.getByRole('region', { name: 'Danger zone' })
    await user.click(within(zone).getByRole('button', { name: 'Delete this speaker' }))
    return screen.findByRole('alertdialog', { name: 'Delete Kitchen?' })
  }

  it('sits at the bottom of the page', async () => {
    renderDevice()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const sections = within(screen.getByRole('tabpanel', { name: 'Overview' })).getAllByRole('region')
    expect(sections.at(-1)).toBe(screen.getByRole('region', { name: 'Danger zone' }))
  })

  it('asks for the name typed, deletes it, goes to the list and never asks the server for it again', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = setupUser({ advanceTimers: vi.advanceTimersByTime })
    // the lights' pending change has the detail read every 2 s while the page shows it
    const { server, router, queryClient } = renderDevice({
      [DELETE]: noContent(),
    })
    const dialog = await openDelete(user)
    act(() => FakeSocket.last.open())
    const field = within(dialog).getByRole('textbox', { name: 'Type Kitchen to confirm' })
    await expectFocusOn(field)
    const confirm = within(dialog).getByRole('button', { name: 'Delete the speaker' })

    await user.click(confirm)
    expect(server.calls(DELETE)).toBe(0)
    expect(within(dialog).getByRole('alert').textContent).toBe('Type Kitchen exactly to confirm.')
    await user.type(field, 'kitchen')
    await user.click(confirm)
    expect(server.calls(DELETE)).toBe(0)

    // by the time its queries are dropped, nothing on the page reads them: none can be fetched again in between
    const device = () => queryClient.getQueryCache().getAll().filter(q => JSON.stringify(q.queryKey).includes(ID))
    const watched: number[][] = []
    const cancel = queryClient.cancelQueries.bind(queryClient)
    vi.spyOn(queryClient, 'cancelQueries').mockImplementation((...args) => {
      watched.push(device().map(q => q.getObserversCount()))
      return cancel(...args)
    })
    await user.clear(field)
    await user.type(field, 'Kitchen')
    await user.click(confirm)
    expect(await screen.findByRole('heading', { level: 1, name: 'Devices' })).toBeTruthy()
    expect(watched).toHaveLength(1)
    expect(watched[0]!.length).toBeGreaterThanOrEqual(3)
    expect(watched[0]!.every(count => count === 0)).toBe(true)
    expect(router.state.location.pathname).toBe('/devices')
    expect(await screen.findByText('Kitchen was deleted')).toBeTruthy()
    await expectFocusOn(screen.getByRole('main'))

    act(() => FakeSocket.last.receive({
      type: 'media_state',
      deviceId: ID,
      at: '2026-11-12T15:05:00Z',
      data: { state: 'idle' },
    }))
    act(() => FakeSocket.last.receive({
      type: 'turn_end',
      deviceId: ID,
      at: '2026-11-12T15:05:00Z',
      data: { code: 'ok' },
    }))
    // the server's own word of the delete, once it committed
    act(() => FakeSocket.last.receive({
      type: 'device_deleted',
      deviceId: ID,
      at: '2026-11-12T15:05:00Z',
    }))
    await act(() => vi.advanceTimersByTimeAsync(35_000))
    const deleted = server.requests.findIndex(r => r.method === 'DELETE')
    const touched = server.requests.slice(deleted + 1).filter(r => new URL(r.url).pathname.includes(ID))
    expect(touched.map(r => `${r.method} ${new URL(r.url).pathname}`)).toEqual([])
    expect(device()).toEqual([])
    // the list and the overview never show it again
    const list = queryClient.getQueryData(devicesQueryOptions(createApiClient()).queryKey)
    expect(list?.map(d => d.id)).toEqual([DEVICES[1]!.id])
  })

  it('drops its health and logs queries too, and none of them asks for it again', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = setupUser({ advanceTimers: vi.advanceTimersByTime })
    const { server, queryClient } = renderDevice({
      [DELETE]: noContent(),
      [`GET /devices/${ID}/metrics`]: metricsRoute,
      [`GET /devices/${ID}/processes`]: json(PROCESSES),
      'GET /logs': json({ logs: [] }),
    }, {
      detail: detail({
        capabilities: [ALARMS, METRICS],
      }),
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    // its health read once, and its lines read on the logs page earlier
    await user.click(screen.getByRole('tab', { name: 'Health' }))
    await screen.findByRole('table', { name: 'Top by CPU' })
    await queryClient.fetchInfiniteQuery(logsQueryOptions(createApiClient({ baseUrl: TEST_BASE_URL, fetch: server.fetch }), { device: ID }))
    // every query whose key names it, however it does
    const reads = (id: string) => queryClient.getQueryCache().getAll().filter(q => JSON.stringify(q.queryKey).includes(id)).map(q => (q.queryKey[0] as { _id: string })._id)
    expect(new Set(reads(ID))).toEqual(new Set([
      'getDevice',
      'getDeviceSettings',
      'getDeviceMedia',
      'getDeviceMusic',
      'listDeviceMetrics',
      'getDeviceProcesses',
      'listLogs',
      'listPlayHistory',
    ]))
    await user.click(screen.getByRole('tab', { name: 'Overview' }))

    const dialog = await openDelete(user)
    await user.type(within(dialog).getByRole('textbox', { name: 'Type Kitchen to confirm' }), 'Kitchen')
    await user.click(within(dialog).getByRole('button', { name: 'Delete the speaker' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Devices' })).toBeTruthy()
    expect(reads(ID)).toEqual([])

    // past every health read's interval
    await act(() => vi.advanceTimersByTimeAsync(5 * 60_000 + 1000))
    expect(reads(ID)).toEqual([])
    const deleted = server.requests.findIndex(r => r.method === 'DELETE')
    expect(server.requests.slice(deleted + 1).filter(r => r.url.includes(ID)).map(r => r.url)).toEqual([])
  })

  it('leaves the page of a device deleted elsewhere, says so, and never asks the server for it again', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { server, router, queryClient } = renderDevice()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
      subscribe: [
        'devices',
        'sessions',
        'agent',
        'media',
        'presence',
        'alarms',
        'ops',
      ],
    }))
    // a change on its way to it, whose window would read it again
    act(() => FakeSocket.last.receive({
      type: 'device_volume',
      deviceId: ID,
      at: '2026-11-12T15:05:00Z',
      data: { level: 20 },
    }))
    const before = server.requests.length
    act(() => FakeSocket.last.receive({
      type: 'device_deleted',
      deviceId: ID,
      at: '2026-11-12T15:05:01Z',
    }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Devices' })).toBeTruthy()
    expect(router.state.location.pathname).toBe('/devices')
    expect(await screen.findByText('Kitchen was deleted elsewhere')).toBeTruthy()
    await expectFocusOn(screen.getByRole('main'))

    await act(() => vi.advanceTimersByTimeAsync(35_000))
    const touched = server.requests.slice(before).filter(r => r.url.includes(ID))
    expect(touched.map(r => `${r.method} ${new URL(r.url).pathname}`)).toEqual([])
    expect(queryClient.getQueryCache().getAll().filter(q => JSON.stringify(q.queryKey).includes(ID))).toEqual([])
    const list = queryClient.getQueryData(devicesQueryOptions(createApiClient()).queryKey)
    expect(list?.map(d => d.id)).toEqual([DEVICES[1]!.id])
  })

  it('words its own delete as its own when the server\'s word of it comes before the answer', async () => {
    const user = setupUser()
    const { server, router } = renderDevice({
      [DELETE]: () => {
        FakeSocket.last.receive({
          type: 'device_deleted',
          deviceId: ID,
          at: '2026-11-12T15:05:00Z',
        })
        return new Response(null, { status: 204 })
      },
    })
    const dialog = await openDelete(user)
    act(() => FakeSocket.last.open())
    await user.type(within(dialog).getByRole('textbox', { name: 'Type Kitchen to confirm' }), 'Kitchen')
    await user.click(within(dialog).getByRole('button', { name: 'Delete the speaker' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/devices'))
    expect(await screen.findByText('Kitchen was deleted')).toBeTruthy()
    expect(screen.queryByText('Kitchen was deleted elsewhere')).toBeNull()
    expect(server.calls(DELETE)).toBe(1)
  })

  it('counts a device the server no longer has as deleted', async () => {
    const user = setupUser()
    const { router } = renderDevice({
      [DELETE]: json({ error: 'not_found' }, 404),
    })
    const dialog = await openDelete(user)
    await user.type(within(dialog).getByRole('textbox', { name: 'Type Kitchen to confirm' }), 'Kitchen')
    await user.click(within(dialog).getByRole('button', { name: 'Delete the speaker' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/devices'))
  })

  it('words a refusal inside the dialog and stays on the page', async () => {
    const user = setupUser()
    const { router, queryClient } = renderDevice({
      [DELETE]: json({ error: 'internal' }, 500),
    })
    const dialog = await openDelete(user)
    await user.type(within(dialog).getByRole('textbox', { name: 'Type Kitchen to confirm' }), 'Kitchen')
    const confirm = within(dialog).getByRole('button', { name: 'Delete the speaker' })
    await user.click(confirm)
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server answered 500 internal.')
    expect(dialog.isConnected).toBe(true)
    expect(document.activeElement).toBe(confirm)
    expect(router.state.location.pathname).toBe(`/devices/${ID}`)
    expect(screen.getByRole('heading', { level: 1, name: 'Kitchen', hidden: true })).toBeTruthy()
    // one lost on its way may still have deleted it
    expect(listInvalidated(queryClient)).toBe(true)
  })
})
