// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiClient } from '@/api/client'
import { deviceQueryOptions } from '@/lib/devices'
import { capability, detail, devicePage, ID, OTHER, PLAYING } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
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

function renderDevice(overrides: Record<string, FakeRoute> = {}, current = {
  detail: detail(),
}) {
  return renderApp({
    path: `/devices/${ID}`,
    routes: devicePage(current, overrides),
    pages: { '/devices/$id': DevicePage },
  })
}

const URL_PLAYER = capability({
  id: 'media.url',
  commands: [
    { name: 'play', argsSchema: null },
  ],
})

function region(name: string) {
  return screen.getByRole('region', { name })
}

describe('a device\'s page', () => {
  it('heads with its name, whether it is online, its board and when it was last seen', async () => {
    renderDevice()
    expect(await screen.findByRole('heading', { level: 1, name: 'Kitchen' })).toBeTruthy()
    // each said once, in the header
    expect(screen.getAllByText('Online')).toHaveLength(1)
    expect(await screen.findByText('Simulator')).toBeTruthy()
    await waitFor(() => expect(screen.getByText('12 Nov, 21:04')).toBeTruthy())
    expect(screen.getByText(ID)).toBeTruthy()
    expect(screen.queryByRole('region', { name: 'Device' })).toBeNull()
  })

  it('names each capability with a heading over its parts', async () => {
    renderDevice()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const alarms = region('alarms')
    expect(within(alarms).getByRole('heading', { level: 3, name: 'alarms' })).toBeTruthy()
    expect(within(alarms).queryByRole('heading', { level: 4 })).toBeNull()
    expect(within(region('display.lights')).getAllByRole('heading', { level: 4 }).map(h => h.textContent)).toEqual(['State', 'Desired configuration'])
  })

  it('starts over for another device, with nothing of the first one open', async () => {
    const user = setupUser()
    const { router, queryClient } = renderDevice({
      [`GET /devices/${OTHER}`]: json(detail({ id: OTHER, name: 'Bedroom' })),
      [`GET /devices/${OTHER}/media`]: json({ state: 'idle' }),
      [`GET /devices/${OTHER}/settings`]: json({}),
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    // read before, so the page swaps without a loading state in between
    queryClient.setQueryData(deviceQueryOptions(createApiClient(), OTHER).queryKey, detail({ id: OTHER, name: 'Bedroom' }))
    await user.click(within(region('display.lights')).getByRole('button', { name: 'Edit the configuration' }))
    await act(() => router.navigate({
      to: '/devices/$id',
      params: { id: OTHER },
    }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Bedroom' })).toBeTruthy()
    expect(within(region('display.lights')).queryByLabelText('Configuration of display.lights (JSON)')).toBeNull()
  })

  it('says whether each capability\'s configuration was applied, is on its way, or failed with the device\'s code', async () => {
    renderDevice()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    expect(within(region('alarms')).getByText('Applied').parentElement?.textContent).toBe('Appliedgen 3')
    expect(within(region('display.lights')).getByText('Pending').parentElement?.textContent).toBe('Pendinggen 4 → 5')
    const failed = within(region('media.soloist')).getByText('Failed').parentElement!
    expect(failed.textContent).toBe('Failedgen 2 key_missing')
    expect(failed.querySelector('s')?.textContent).toBe('gen 2')
    expect(within(region('audio.output')).getByText('Device defaults')).toBeTruthy()
  })

  it('shows each capability\'s state as the device reports it', async () => {
    renderDevice()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const state = within(region('audio.output')).getByRole('group', { name: 'State' })
    expect(within(state).getAllByRole('term').map(t => t.textContent)).toEqual(['level', 'channels', 'maxBitrate'])
    expect(within(state).getAllByRole('definition').map(t => t.textContent)).toEqual(['30', '1', '64000'])
  })

  it('reads the detail again soon while a change is on its way', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const current = {
      detail: detail(),
    }
    const { server } = renderDevice({}, current)
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    current.detail = detail({
      capabilities: detail().capabilities!.map(c => c.id === 'display.lights'
        ? { ...c, appliedGen: 5 }
        : c),
    })
    await act(() => vi.advanceTimersByTimeAsync(2000))
    await waitFor(() => expect(within(region('display.lights')).getByText('Applied')).toBeTruthy())
    expect(server.calls(`GET /devices/${ID}`)).toBeGreaterThanOrEqual(2)
  })

  it('keeps the slow beat for an offline device, whose change cannot arrive', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { server } = renderDevice({}, {
      detail: detail({ online: false }),
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    await act(() => vi.advanceTimersByTimeAsync(10_000))
    expect(server.calls(`GET /devices/${ID}`)).toBe(1)
  })

  it('shows what is playing, with no cover when the session names none', async () => {
    renderDevice({
      [`GET /devices/${ID}/media`]: json(PLAYING),
    })
    const media = await screen.findByRole('region', { name: 'Media' })
    expect(await within(media).findByText('Clair de lune')).toBeTruthy()
    expect(within(media).getByText('Claude Debussy · Suite bergamasque')).toBeTruthy()
    expect(within(media).getByText('Playing')).toBeTruthy()
    expect(within(media).getByText('Spotify Connect')).toBeTruthy()
    expect(within(media).getByText('5:02')).toBeTruthy()
    expect(within(media).getByRole('progressbar', { name: 'Position' }).getAttribute('aria-valuemax')).toBe('302')
    expect(media.querySelector('img')).toBeNull()
  })

  it('shows the cover through the server, pinned to the session\'s hash', async () => {
    const hash = 'a'.repeat(64)
    renderDevice({
      [`GET /devices/${ID}/media`]: json({
        ...PLAYING,
        source: 'airplay',
        metadata: { ...PLAYING.metadata, artworkUrl: '', artworkHash: hash },
      }),
    })
    const media = await screen.findByRole('region', { name: 'Media' })
    await within(media).findByText('Clair de lune')
    const img = media.querySelector('img')!
    expect(img.getAttribute('src')).toBe(`/api/v1/devices/${encodeURIComponent(ID)}/artwork?h=${hash}`)
    // a cover that fails gives way to the note
    fireEvent.error(img)
    expect(media.querySelector('img')).toBeNull()
  })

  it('says when nothing is playing', async () => {
    renderDevice()
    const media = await screen.findByRole('region', { name: 'Media' })
    expect(await within(media).findByText('Nothing is playing.')).toBeTruthy()
  })

  it('follows this device\'s turns, steps and media on the live feed, and only this device\'s', async () => {
    renderDevice()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames()).toEqual([
      {
        subscribe: [
          'devices',
          'sessions',
          'agent',
          'media',
          'presence',
          'alarms',
          'ops',
        ],
      },
    ]))
    const send = (frame: object) => act(() => FakeSocket.last.receive(frame))
    send({
      type: 'turn_start',
      deviceId: ID,
      at: '2026-11-12T15:04:10Z',
      data: { trigger: 'wake' },
    })
    send({
      type: 'transcript',
      deviceId: OTHER,
      at: '2026-11-12T15:04:11Z',
      data: { text: 'not here' },
    })
    send({
      type: 'transcript',
      deviceId: ID,
      at: '2026-11-12T15:04:11Z',
      data: { text: 'play something' },
    })
    send({
      type: 'media_state',
      deviceId: ID,
      at: '2026-11-12T15:04:13Z',
      data: PLAYING,
    })
    // the same state again (a seek, a volume) is no new stop
    send({
      type: 'media_state',
      deviceId: ID,
      at: '2026-11-12T15:04:14Z',
      data: { ...PLAYING, positionMs: 1000 },
    })
    send({
      type: 'media_state',
      deviceId: ID,
      at: '2026-11-12T15:04:20Z',
      data: { state: 'idle' },
    })

    const timeline = screen.getByRole('list', { name: 'Kitchen activity' })
    const stops = within(timeline).getAllByRole('listitem').map(li => li.textContent)
    expect(stops).toHaveLength(4)
    expect(stops[0]).toContain('21:04:20')
    expect(stops[0]).toContain('Media stopped')
    expect(stops[1]).toContain('Playing «Clair de lune» · Claude Debussy')
    expect(stops[2]).toContain('Heard: «play something»')
    expect(stops[3]).toContain('Woken by voice')
    expect(stops.join('')).not.toContain('not here')
    expect(stops.join('')).not.toContain('Kitchen ·')
  })

  it('reads the device again when it is renamed elsewhere, goes offline or its volume moves, and not for another', async () => {
    const current = {
      detail: detail(),
    }
    const { server } = renderDevice({}, current)
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    act(() => FakeSocket.last.open())
    const reads = () => server.calls(`GET /devices/${ID}`)
    const send = (type: string, deviceId: string, data: object) => act(() => FakeSocket.last.receive({
      type,
      deviceId,
      at: '2026-11-12T15:05:00Z',
      data,
    }))

    current.detail = detail({ name: 'Pantry' })
    send('device_renamed', ID, { name: 'Pantry', model: 'sim', online: true })
    expect(await screen.findByRole('heading', { level: 1, name: 'Pantry' })).toBeTruthy()
    expect(reads()).toBe(2)

    current.detail = detail({ name: 'Pantry', online: false })
    send('device_offline', ID, { name: 'Pantry', model: 'sim', online: false })
    await waitFor(() => expect(reads()).toBe(3))

    send('device_volume', ID, { level: 20 })
    await waitFor(() => expect(reads()).toBe(4))

    send('device_renamed', OTHER, { name: 'Hall', model: 'sim', online: true })
    send('device_volume', OTHER, { level: 20 })
    send('device_vitals', ID, {
      cpu: 0.1,
      memoryUsedBytes: 1,
      memoryLimitBytes: 2,
      disk: [],
      at: '2026-11-12T15:05:00Z',
    })
    await new Promise(resolve => setTimeout(resolve, 600))
    expect(reads()).toBe(4)
  })

  it('plays a url on the speaker as its media session', async () => {
    const user = setupUser()
    const url = 'http://ice1.somafm.com/dronezone-256-mp3'
    const { server } = renderDevice({
      [`POST /devices/${ID}/capabilities/media.url/commands/play`]: json({ result: null }),
    }, {
      detail: detail({
        capabilities: [URL_PLAYER],
      }),
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const box = region('Play a URL')
    const field = within(box).getByLabelText('Stream or file URL')
    await user.type(field, 'ftp://x')
    expect(within(box).getByText('Starts with http:// or https://.')).toBeTruthy()
    await user.click(within(box).getByRole('button', { name: 'Play' }))
    expect(server.requests.some(r => r.method === 'POST')).toBe(false)

    await user.clear(field)
    await user.type(field, `  ${url} `)
    await user.click(within(box).getByRole('button', { name: 'Play' }))
    expect(await within(box).findByText('Playing on Kitchen.')).toBeTruthy()
    const post = server.requests.find(r => r.method === 'POST')!
    expect(await post.clone().json()).toEqual({ url, mode: 'session' })
  })

  it('says why the url could not play, in codes', async () => {
    const user = setupUser()
    renderDevice({
      [`POST /devices/${ID}/capabilities/media.url/commands/play`]: json({ error: 'device_offline' }, 409),
    }, {
      detail: detail({
        capabilities: [URL_PLAYER],
      }),
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const box = region('Play a URL')
    await user.type(within(box).getByLabelText('Stream or file URL'), 'https://radio.example/stream')
    await user.click(within(box).getByRole('button', { name: 'Play' }))
    expect((await within(box).findByRole('alert')).textContent).toBe('The server answered 409 device_offline.')
  })

  it('offers no url box without media.url, or on a screen', async () => {
    renderDevice({}, {
      detail: detail({
        capabilities: [
          URL_PLAYER,
          capability({ id: 'display.screen' }),
        ],
      }),
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    expect(screen.queryByRole('region', { name: 'Play a URL' })).toBeNull()
    cleanup()
    renderDevice()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    expect(screen.queryByRole('region', { name: 'Play a URL' })).toBeNull()
  })

  it('says something on the speaker', async () => {
    const user = setupUser()
    const { server } = renderDevice({
      [`POST /devices/${ID}/announce`]: json({ streamId: '0193a71e-0000-7000-8000-000000000001' }, 202),
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const say = region('Say')
    await user.type(within(say).getByLabelText('Text to say'), 'Dinner is ready')
    await user.click(within(say).getByRole('button', { name: 'Say it' }))
    expect(await within(say).findByText('Sent to Kitchen.')).toBeTruthy()
    const post = server.requests.find(r => r.method === 'POST')!
    expect(await post.clone().json()).toEqual({ text: 'Dinner is ready' })
    expect((within(say).getByLabelText('Text to say') as HTMLTextAreaElement).value).toBe('')
  })

  it('says why the speaker could not say it, in codes', async () => {
    const user = setupUser()
    renderDevice({
      [`POST /devices/${ID}/announce`]: json({ error: 'device_offline' }, 409),
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const say = region('Say')
    await user.type(within(say).getByLabelText('Text to say'), 'Dinner is ready')
    await user.click(within(say).getByRole('button', { name: 'Say it' }))
    expect((await within(say).findByRole('alert')).textContent).toBe('The server answered 409 device_offline.')
  })

  it('keeps the focus on Say it while it is sent, after a refusal and once sent', async () => {
    const user = setupUser()
    const answers: ((response: Response) => void)[] = []
    renderDevice({
      [`POST /devices/${ID}/announce`]: () => new Promise<Response>((resolve) => {
        answers.push(resolve)
      }),
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const say = region('Say')
    const send = within(say).getByRole('button', { name: 'Say it' })
    await user.type(within(say).getByLabelText('Text to say'), 'Dinner is ready')
    await user.click(send)
    await waitFor(() => expect(answers).toHaveLength(1))
    expect(send.getAttribute('aria-disabled')).toBe('true')
    expect((send as HTMLButtonElement).disabled).toBe(false)
    await user.click(send)
    expect(answers).toHaveLength(1)

    answers[0]!(Response.json({ error: 'device_offline' }, { status: 409 }))
    await within(say).findByRole('alert')
    expect(document.activeElement).toBe(send)
    expect(send.getAttribute('aria-disabled')).toBeNull()

    await user.click(send)
    await waitFor(() => expect(answers).toHaveLength(2))
    answers[1]!(Response.json({ streamId: '0193a71e-0000-7000-8000-000000000001' }, { status: 202 }))
    await within(say).findByText('Sent to Kitchen.')
    // the field is empty again: nothing to send, and the focus still where it was
    expect(document.activeElement).toBe(send)
    expect(send.getAttribute('aria-disabled')).toBe('true')
  })

  it('says the device is gone when the server no longer has it', async () => {
    renderDevice({
      [`GET /devices/${ID}`]: json({ error: 'not_found' }, 404),
    })
    expect((await screen.findByRole('alert')).textContent).toContain('The server answered 404 not_found.')
    expect(within(screen.getByRole('main')).getByRole('link', { name: 'Devices' }).getAttribute('href')).toBe('/devices')
  })

  it('has no accessibility violations axe can see without layout', async () => {
    renderDevice({
      [`GET /devices/${ID}/media`]: json(PLAYING),
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    await within(region('Media')).findByText('Clair de lune')
    // jsdom has no canvas; axe only probes for one
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
    const results = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
      },
    })
    expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
  })
})
