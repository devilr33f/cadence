// @vitest-environment jsdom
import type { Detail } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiClient } from '@/api/client'
import { devicesQueryOptions } from '@/lib/devices'
import { capability, detail, devicePage, ID } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { DEVICES } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { DevicePage } from '../detail/device-page'

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

const KEY = `POST /devices/${ID}/capabilities/input.remote/commands/key`
const PLAY = `POST /devices/${ID}/video`

const SCREEN = capability({
  id: 'display.screen',
  state: { power: 'on', screen: true },
})
const REMOTE = capability({
  id: 'input.remote',
  state: { lastSource: 'api' },
  commands: [
    { name: 'key', argsSchema: null },
  ],
})
const VIDEO = capability({
  id: 'media.video',
  state: { tracks: [] },
})
const AIRPLAY = capability({
  id: 'media.screencast',
  state: {
    enabled: true,
    state: 'ready',
    mirroring: true,
    clientName: 'iPhone',
  },
})

function tv(): Detail {
  return detail({
    name: 'Living TV',
    model: 'sim-tv',
    capabilities: [SCREEN, REMOTE, VIDEO, AIRPLAY],
  })
}

function renderTv(overrides: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: tv(),
    }, {
      [`GET /devices/${ID}/video`]: json({ renditions: [] }),
      ...overrides,
    }),
    pages: { '/devices/$id': DevicePage },
  })
  app.queryClient.setQueryData(devicesQueryOptions(createApiClient()).queryKey, [
    { ...DEVICES[0]!, id: ID },
  ])
  return app
}

describe('a tv\'s section', () => {
  it('is not shown for a speaker', async () => {
    renderApp({
      path: `/devices/${ID}`,
      routes: devicePage({
        detail: detail(),
      }),
      pages: { '/devices/$id': DevicePage },
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    expect(screen.queryByRole('group', { name: 'Remote pad' })).toBeNull()
  })

  it('sends each key of the pad as input.remote key, and says so', async () => {
    const user = setupUser()
    const sent: unknown[] = []
    renderTv({
      [KEY]: async (request) => {
        sent.push(await request.clone().json())
        return Response.json({ result: null })
      },
    })
    const pad = await screen.findByRole('group', { name: 'Remote pad' })
    await user.click(within(pad).getByRole('button', { name: 'Down' }))
    await screen.findByText('Down sent.')
    await user.click(within(pad).getByRole('button', { name: 'OK' }))
    await screen.findByText('OK sent.')
    await user.click(within(pad).getByRole('button', { name: 'Back' }))
    await screen.findByText('Back sent.')
    // the arrow keys and Enter while the pad has the focus
    pad.focus()
    await user.keyboard('{ArrowLeft}')
    await screen.findByText('Left sent.')
    expect(sent).toEqual([
      { key: 'down' },
      { key: 'ok' },
      { key: 'back' },
      { key: 'left' },
    ])
    // the pad never raises the level
    expect(within(pad).queryByRole('button', { name: /volume up/i })).toBeNull()
  })

  it('words a key the device refused', async () => {
    const user = setupUser()
    renderTv({
      [KEY]: json({ error: 'device_offline' }, 409),
    })
    const pad = await screen.findByRole('group', { name: 'Remote pad' })
    await user.click(within(pad).getByRole('button', { name: 'Home' }))
    expect((await screen.findByRole('alert')).textContent).toMatch(/^Home failed\./)
  })

  it('plays a youtube link on the tv alone, and maps its refusals', async () => {
    const user = setupUser()
    const bodies: unknown[] = []
    let answer: FakeRoute = json({
      mode: 'video',
      height: 1080,
      renditions: 4,
      view: 'video',
      title: 'A talk',
    })
    renderTv({
      [PLAY]: async (request) => {
        bodies.push(await request.clone().json())
        return answer(request)
      },
    })
    const field = await screen.findByRole('textbox', { name: 'YouTube link' })
    await user.type(field, 'https://youtu.be/abc')
    await user.click(screen.getByRole('button', { name: 'Play on the TV' }))
    await screen.findByText('Playing «A talk» at 1080p.')
    expect(bodies).toEqual([
      { url: 'https://youtu.be/abc', screenOnly: true },
    ])
    for (const [code, status, words] of [
      ['invalid_url', 422, /not a YouTube video link/],
      ['no_screen', 409, /no screen/],
      ['sign_in_required', 409, /signed-in viewer/],
      ['not_set_up', 503, /No video player is set up/],
    ] as const) {
      answer = json({ error: code }, status)
      await user.type(field, 'https://example.com/x')
      await user.click(screen.getByRole('button', { name: 'Play on the TV' }))
      await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(words))
      expect(screen.getByRole('alert').textContent).toContain(`(${code})`)
      await user.clear(field)
    }
  })

  it('shows airplay\'s state and who mirrors', async () => {
    renderTv()
    const region = await screen.findByRole('region', { name: 'AirPlay' })
    expect(region.textContent).toContain('Ready')
    expect(region.textContent).toContain('Mirroring from iPhone')
  })
})
