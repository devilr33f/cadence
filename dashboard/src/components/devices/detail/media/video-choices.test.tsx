// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { detail, devicePage, ID, PLAYING } from '@/test/device-detail'
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
  vi.restoreAllMocks()
})

const SESSION = {
  ...PLAYING,
  sessionId: 'video-3',
  source: 'video',
  metadata: {
    title: 'A Song',
    artist: 'Artist',
    album: 'LP',
    artworkUrl: 'https://cadence.example/v2/images/abc',
    kind: 'track',
    providerName: 'YouTube Music',
  },
  actions: ['pause', 'resume', 'stop', 'seek'],
}

const VIDEO = {
  sessionId: 'video-3',
  title: 'A Song',
  channel: 'Artist - Topic',
  description: 'About the song.',
  music: true,
  view: 'visualizer',
  views: ['video', 'audio', 'visualizer'],
  renditions: [
    {
      id: '2160p60',
      height: 2160,
      fps: 60,
      vcodec: 'vp9',
      hdr: 'HDR10',
      selected: true,
    },
    {
      id: '1080p',
      height: 1080,
      vcodec: 'h264',
      selected: false,
    },
  ],
  audio: [
    {
      id: 'a-ru',
      language: 'ru',
      label: 'Russian',
      dubbed: true,
      selected: true,
    },
    {
      id: 'a-en',
      language: 'en',
      label: 'English',
      original: true,
    },
  ],
}

function renderCard(video: unknown, overrides: Record<string, FakeRoute> = {}) {
  return renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: detail(),
    }, {
      [`GET /devices/${ID}/media`]: json(SESSION),
      [`GET /devices/${ID}/video`]: json(video),
      ...overrides,
    }),
    pages: { '/devices/$id': DevicePage },
  })
}

describe('the media card of a video the server plays', () => {
  it('shows its description, view, qualities and audio languages, and switches them', async () => {
    const user = setupUser()
    const sent: unknown[] = []
    let current = VIDEO
    renderCard(VIDEO, {
      [`GET /devices/${ID}/video`]: () => Response.json(current),
      [`PATCH /devices/${ID}/video`]: async (request) => {
        const body = await request.clone().json() as Record<string, string>
        sent.push(body)
        current = { ...current, view: body.view ?? current.view }
        return Response.json(current)
      },
    })
    const card = await screen.findByRole('region', { name: 'Media' })
    const view = await within(card).findByRole('group', { name: 'View' })
    expect(within(card).getByText('About the song.')).toBeTruthy()
    expect(within(view).getByRole('button', { name: 'Audio + visualizer' }).getAttribute('aria-pressed')).toBe('true')
    const quality = within(card).getByRole('group', { name: 'Quality' })
    // in an audio view no quality plays
    expect(within(quality).getByRole('button', { name: /2160p60/ }).getAttribute('aria-pressed')).toBe('false')
    expect(within(quality).getByRole('button', { name: /2160p60/ }).textContent).toContain('vp9 · HDR10')
    const audio = within(card).getByRole('group', { name: 'Audio' })
    expect(within(audio).getByRole('button', { name: 'Russian · dub' }).getAttribute('aria-pressed')).toBe('true')
    await user.click(within(audio).getByRole('button', { name: 'English · original' }))
    await user.click(within(view).getByRole('button', { name: 'Video' }))
    await waitFor(() => expect(sent).toEqual([
      { audio: 'a-en' },
      { view: 'video' },
    ]))
    await waitFor(() => expect(within(view).getByRole('button', { name: 'Video' }).getAttribute('aria-pressed')).toBe('true'))
  })

  it('shows nothing of a video that is not the device\'s session any more', async () => {
    renderCard({ ...VIDEO, sessionId: 'video-2' })
    const card = await screen.findByRole('region', { name: 'Media' })
    await within(card).findByText('A Song')
    expect(within(card).queryByRole('group', { name: 'Quality' })).toBeNull()
    expect(within(card).queryByText('About the song.')).toBeNull()
  })

  it('words a refused switch', async () => {
    const user = setupUser()
    renderCard(VIDEO, {
      [`PATCH /devices/${ID}/video`]: json({ error: 'device_offline' }, 409),
    })
    const audio = await screen.findByRole('group', { name: 'Audio' })
    await user.click(within(audio).getByRole('button', { name: 'English · original' }))
    expect((await screen.findByRole('alert')).textContent).toMatch(/^The video was not switched\./)
  })
})
