// @vitest-environment jsdom
import type { View } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { capability, detail, devicePage, ID, OUTPUT, PLAYING } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { DevicePage } from './device-page'

const COMMANDS = `/devices/${ID}/capabilities`

const QUEUE: View = {
  active: true,
  current: { title: 'Clair de lune', artist: 'Claude Debussy', provider: 'YouTube Music' },
  upcoming: [
    { title: 'Gymnopédie No. 1', artist: 'Erik Satie', durationS: 185 },
    { title: 'Rêverie', artist: 'Claude Debussy', album: 'Piano works' },
  ],
  history: [
    { title: 'Arabesque No. 1', artist: 'Claude Debussy' },
  ],
  shuffle: false,
  repeat: 'off',
  radio: false,
}

const QUEUED = {
  ...PLAYING,
  source: 'stream',
  provider: 'YouTube Music',
  webUrl: 'https://music.youtube.example/watch?v=abc',
  actions: [
    'pause',
    'stop',
    'seek',
    'next',
    'previous',
  ],
}

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

// a route that keeps each body it was sent and answers `body`
function recording(body: unknown = { result: null }, status = 200) {
  const bodies: unknown[] = []
  const route: FakeRoute = async (request) => {
    bodies.push(await request.json())
    return Response.json(body, { status })
  }
  return { bodies, route }
}

function renderMedia(overrides: Record<string, FakeRoute> = {}, current = {
  detail: detail(),
}) {
  return renderApp({
    path: `/devices/${ID}`,
    routes: devicePage(current, overrides),
    pages: { '/devices/$id': DevicePage },
  })
}

async function media() {
  return screen.findByRole('region', { name: 'Media' })
}

describe('the media card\'s controls', () => {
  it('offers only what the session allows, and sends each press as a media.player command', async () => {
    const pause = recording()
    const next = recording()
    const { server } = renderMedia({
      [`GET /devices/${ID}/media`]: json(PLAYING),
      [`POST ${COMMANDS}/media.player/commands/pause`]: pause.route,
      [`POST ${COMMANDS}/media.player/commands/next`]: next.route,
    })
    const card = await media()
    const playback = await within(card).findByRole('group', { name: 'Playback' })
    expect(within(playback).getAllByRole('button').map(b => b.getAttribute('aria-label'))).toEqual(['Previous', 'Pause', 'Next'])
    // no seek in its actions: the position only shows
    expect(within(card).getByRole('progressbar', { name: 'Position' })).toBeTruthy()

    const user = setupUser()
    const reads = server.calls(`GET /devices/${ID}/media`)
    await user.click(within(playback).getByRole('button', { name: 'Pause' }))
    await waitFor(() => expect(pause.bodies).toEqual([{}]))
    await waitFor(() => expect(server.calls(`GET /devices/${ID}/media`)).toBeGreaterThan(reads))
    await user.click(within(playback).getByRole('button', { name: 'Next' }))
    await waitFor(() => expect(next.bodies).toEqual([{}]))
  })

  it('plays a paused session with resume, and stops it', async () => {
    const resume = recording()
    const stop = recording()
    renderMedia({
      [`GET /devices/${ID}/media`]: json({
        ...PLAYING,
        state: 'paused',
        actions: ['resume', 'stop'],
      }),
      [`POST ${COMMANDS}/media.player/commands/resume`]: resume.route,
      [`POST ${COMMANDS}/media.player/commands/stop`]: stop.route,
    })
    const playback = await within(await media()).findByRole('group', { name: 'Playback' })
    expect(within(playback).getAllByRole('button').map(b => b.getAttribute('aria-label'))).toEqual(['Play', 'Stop'])
    const user = setupUser()
    await user.click(within(playback).getByRole('button', { name: 'Play' }))
    await waitFor(() => expect(resume.bodies).toHaveLength(1))
    await user.click(within(playback).getByRole('button', { name: 'Stop' }))
    await waitFor(() => expect(stop.bodies).toHaveLength(1))
  })

  it('says a refused command in codes beside the controls', async () => {
    renderMedia({
      [`GET /devices/${ID}/media`]: json(PLAYING),
      [`POST ${COMMANDS}/media.player/commands/pause`]: json({ error: 'device_offline' }, 409),
    })
    const card = await media()
    const user = setupUser()
    await user.click(await within(card).findByRole('button', { name: 'Pause' }))
    expect((await within(card).findByRole('alert')).textContent).toBe('Pause failed. The server answered 409 device_offline.')
  })

  it('seeks to where the position slider rests, once', async () => {
    const seek = recording()
    renderMedia({
      [`GET /devices/${ID}/media`]: json(QUEUED),
      [`GET /devices/${ID}/music`]: json(QUEUE),
      [`POST ${COMMANDS}/media.player/commands/seek`]: seek.route,
    })
    const slider = await within(await media()).findByRole('slider', { name: 'Position' })
    expect(slider.getAttribute('max')).toBe('302')
    fireEvent.change(slider, {
      target: { value: '20' },
    })
    fireEvent.change(slider, {
      target: { value: '30' },
    })
    expect(slider.getAttribute('aria-valuetext')).toBe('0:30 of 5:02')
    await waitFor(() => expect(seek.bodies).toEqual([
      { positionMs: 30_000 },
    ]))
  })
})

describe('the media card\'s volume', () => {
  it('shows the device\'s level and sets the final value of a drag with set_level', async () => {
    const setLevel = recording({
      result: { level: 50 },
    })
    const current = {
      detail: detail(),
    }
    const { server } = renderMedia({
      [`POST ${COMMANDS}/audio.output/commands/set_level`]: setLevel.route,
    }, current)
    const slider = await within(await media()).findByRole('slider', { name: 'Volume' }) as HTMLInputElement
    expect(slider.value).toBe('30')
    // nothing plays, and the level is still there
    expect(within(await media()).getByText('Nothing is playing.')).toBeTruthy()

    const reads = server.calls(`GET /devices/${ID}`)
    current.detail = detail({
      capabilities: [
        capability({
          ...OUTPUT,
          state: { level: 50 },
        }),
      ],
    })
    fireEvent.change(slider, {
      target: { value: '45' },
    })
    fireEvent.change(slider, {
      target: { value: '50' },
    })
    expect(slider.getAttribute('aria-valuetext')).toBe('50%')
    await waitFor(() => expect(setLevel.bodies).toEqual([
      { level: 50 },
    ]))
    await waitFor(() => expect(server.calls(`GET /devices/${ID}`)).toBeGreaterThan(reads))
    await waitFor(() => expect(slider.value).toBe('50'))
  })

  it('says a refused level and goes back to the device\'s', async () => {
    renderMedia({
      [`POST ${COMMANDS}/audio.output/commands/set_level`]: json({ error: 'wake_required' }, 409),
    })
    const card = await media()
    const slider = await within(card).findByRole('slider', { name: 'Volume' }) as HTMLInputElement
    fireEvent.change(slider, {
      target: { value: '80' },
    })
    const alert = await within(card).findByRole('alert')
    expect(alert.textContent).toBe('The volume could not be set. The server answered 409 wake_required.')
    expect(slider.getAttribute('aria-describedby')).toBe(alert.id)
    expect(slider.value).toBe('30')
  })

  it('is left out where the device reports no level', async () => {
    renderMedia({}, {
      detail: detail({ capabilities: [] }),
    })
    const card = await media()
    await within(card).findByText('Nothing is playing.')
    expect(within(card).queryByRole('slider', { name: 'Volume' })).toBeNull()
  })
})

describe('the media card\'s queue', () => {
  it('toggles shuffle and steps repeat through off, all and one', async () => {
    // the server's queue as the next read answers it: a change lands there too
    let queue = QUEUE
    const modes: unknown[] = []
    renderMedia({
      [`GET /devices/${ID}/media`]: json(QUEUED),
      [`GET /devices/${ID}/music`]: () => Response.json(queue),
      [`PUT /devices/${ID}/music/modes`]: async (request) => {
        const body = await request.json() as Partial<View>
        modes.push(body)
        queue = {
          ...queue,
          ...body,
        }
        return Response.json(queue)
      },
    })
    const card = await media()
    const shuffle = await within(card).findByRole('button', { name: 'Shuffle' })
    expect(shuffle.getAttribute('aria-pressed')).toBe('false')
    const user = setupUser()
    await user.click(shuffle)
    await waitFor(() => expect(modes).toEqual([
      { shuffle: true },
    ]))
    await waitFor(() => expect(shuffle.getAttribute('aria-pressed')).toBe('true'))

    await user.click(within(card).getByRole('button', { name: 'Repeat: off' }))
    await waitFor(() => expect(modes.at(-1)).toEqual({ repeat: 'all' }))
    await user.click(await within(card).findByRole('button', { name: 'Repeat: the queue' }))
    await waitFor(() => expect(modes.at(-1)).toEqual({ repeat: 'one' }))
    await user.click(await within(card).findByRole('button', { name: 'Repeat: this song' }))
    await waitFor(() => expect(modes.at(-1)).toEqual({ repeat: 'off' }))
  })

  it('names the mode in force on the repeat button', async () => {
    renderMedia({
      [`GET /devices/${ID}/media`]: json(QUEUED),
      [`GET /devices/${ID}/music`]: json({ ...QUEUE, repeat: 'one' }),
    })
    const repeat = await within(await media()).findByRole('button', { name: 'Repeat: this song' })
    expect(repeat.getAttribute('title')).toBe('Repeat off next')
  })

  it('lists what comes next and what played, and plays a song further down', async () => {
    const jump = recording({ ...QUEUE, upcoming: [] })
    renderMedia({
      [`GET /devices/${ID}/media`]: json(QUEUED),
      [`GET /devices/${ID}/music`]: json(QUEUE),
      [`POST /devices/${ID}/music/jump`]: jump.route,
    })
    const card = await media()
    const toggle = await within(card).findByRole('button', { name: 'Queue, 2 up next' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    const user = setupUser()
    await user.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    const next = within(card).getByRole('region', { name: 'Up next' })
    expect(within(next).getAllByRole('button').map(b => b.getAttribute('aria-label'))).toEqual([
      'Play Gymnopédie No. 1 by Erik Satie',
      'Play Rêverie by Claude Debussy',
    ])
    expect(within(next).getByText('3:05')).toBeTruthy()
    expect(within(within(card).getByRole('region', { name: 'Recently played' })).getByText('Arabesque No. 1')).toBeTruthy()

    await user.click(within(next).getByRole('button', { name: 'Play Rêverie by Claude Debussy' }))
    await waitFor(() => expect(jump.bodies).toEqual([
      { offset: 2 },
    ]))
  })

  it('says a refused jump in codes', async () => {
    renderMedia({
      [`GET /devices/${ID}/media`]: json(QUEUED),
      [`GET /devices/${ID}/music`]: json(QUEUE),
      [`POST /devices/${ID}/music/jump`]: json({ error: 'queue_end' }, 409),
    })
    const card = await media()
    const user = setupUser()
    await user.click(await within(card).findByRole('button', { name: 'Queue, 2 up next' }))
    await user.click(within(card).getByRole('button', { name: 'Play Gymnopédie No. 1 by Erik Satie' }))
    expect((await within(card).findByRole('alert')).textContent).toBe('Playing from the queue failed. The server answered 409 queue_end.')
  })

  it('shows nothing of the queue while the server holds none', async () => {
    renderMedia({
      [`GET /devices/${ID}/media`]: json(PLAYING),
    })
    const card = await media()
    await within(card).findByText('Clair de lune')
    expect(within(card).queryByRole('button', { name: 'Shuffle' })).toBeNull()
    expect(within(card).queryByRole('button', { name: /^Queue/ })).toBeNull()
  })

  it('reads the queue again on this device\'s media events', async () => {
    const { server } = renderMedia({
      [`GET /devices/${ID}/media`]: json(QUEUED),
      [`GET /devices/${ID}/music`]: json(QUEUE),
    })
    await within(await media()).findByRole('button', { name: 'Shuffle' })
    act(() => FakeSocket.last.open())
    const reads = server.calls(`GET /devices/${ID}/music`)
    act(() => FakeSocket.last.receive({
      type: 'media_state',
      deviceId: ID,
      at: '2026-11-12T15:04:13Z',
      data: QUEUED,
    }))
    await waitFor(() => expect(server.calls(`GET /devices/${ID}/music`)).toBe(reads + 1))
  })

  it('says when the queue could not be read', async () => {
    renderMedia({
      [`GET /devices/${ID}/music`]: json({ error: 'forbidden' }, 403),
    })
    const card = await media()
    expect((await within(card).findByRole('alert')).textContent).toContain('The music queue could not be read.')
  })
})

describe('the media card\'s source', () => {
  it('names the provider of a queued song and links its page in a new tab', async () => {
    renderMedia({
      [`GET /devices/${ID}/media`]: json(QUEUED),
    })
    const card = await media()
    expect(await within(card).findByText('YouTube Music')).toBeTruthy()
    const link = within(card).getByRole('link', { name: 'Open in YouTube Music (new tab)' })
    expect(link.getAttribute('href')).toBe(QUEUED.webUrl)
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.getAttribute('rel')).toBe('noopener noreferrer')
  })

  it.each([
    ['soloist', 'Spotify Connect'],
    ['airplay', 'AirPlay'],
    ['bluetooth', 'Bluetooth'],
    ['url', 'URL'],
  ])('labels a %s session %s, with no link', async (source, label) => {
    renderMedia({
      [`GET /devices/${ID}/media`]: json({ ...PLAYING, source }),
    })
    const card = await media()
    expect(await within(card).findByText(label)).toBeTruthy()
    expect(within(card).queryByRole('link')).toBeNull()
  })

  it('opens no page that is not a web address', async () => {
    renderMedia({
      [`GET /devices/${ID}/media`]: json({ ...QUEUED, webUrl: 'javascript:alert(1)' }),
    })
    const card = await media()
    await within(card).findByText('YouTube Music')
    expect(within(card).queryByRole('link')).toBeNull()
  })
})

it('has no accessibility violations axe can see with every control and the queue open', async () => {
  renderMedia({
    [`GET /devices/${ID}/media`]: json(QUEUED),
    [`GET /devices/${ID}/music`]: json(QUEUE),
  })
  const card = await media()
  const user = setupUser()
  await user.click(await within(card).findByRole('button', { name: 'Queue, 2 up next' }))
  await within(card).findByRole('slider', { name: 'Volume' })
  // jsdom has no canvas; axe only probes for one
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  const results = await axe.run(card, {
    rules: {
      'color-contrast': { enabled: false },
    },
  })
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})
