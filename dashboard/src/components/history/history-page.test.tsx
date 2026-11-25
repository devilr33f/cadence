// @vitest-environment jsdom
import type { ReactElement } from 'react'
import type { HistoryEntry } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { BEDROOM, DEVICES, KITCHEN, queryOf } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { HistoryPage } from './history-page'
import { RecentlyPlayed } from './recently-played'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const ONE: HistoryEntry = {
  id: 1,
  deviceId: KITCHEN,
  deviceName: 'Kitchen',
  source: 'music',
  provider: 'ytmusic',
  providerSongId: '5qZQEq_C3vc',
  webUrl: 'https://music.youtube.com/watch?v=5qZQEq_C3vc',
  feedback: 'none',
  favorite: false,
  kind: 'track',
  title: 'One',
  artist: 'Artist',
  album: 'Album',
  artworkUrl: 'https://img.example/one.jpg',
  startedAt: '2026-11-16T10:00:00Z',
  endedAt: '2026-11-16T10:01:05Z',
  playedMs: 65_000,
}

const TWO: HistoryEntry = {
  id: 2,
  deviceId: BEDROOM,
  deviceName: 'Bedroom',
  source: 'airplay',
  feedback: 'none',
  favorite: false,
  kind: 'track',
  title: 'Two',
  artist: 'Another artist',
  artworkHash: 'a'.repeat(64),
  startedAt: '2026-11-16T11:00:00Z',
  endedAt: '2026-11-16T11:02:02Z',
  playedMs: 122_000,
}

function historyRoute(items: HistoryEntry[] = [ONE, TWO]): FakeRoute {
  return (request) => {
    const query = queryOf(request)
    const deviceId = query.deviceId
    const limit = Number(query.limit ?? 50)
    const filtered = items.filter(item => !deviceId || item.deviceId === deviceId)
    return Response.json({
      items: filtered.slice(0, limit),
      ...(filtered.length > limit && { next: 'next-cursor' }),
    })
  }
}

function renderHistory(routes: Record<string, FakeRoute> = {}, component: () => ReactElement = HistoryPage) {
  return {
    ...renderApp({
      path: '/history',
      routes: {
        'GET /session': signedIn,
        'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
        'GET /devices': json(DEVICES),
        'GET /history': historyRoute(),
        ...routes,
      },
      pages: { '/history': component },
    }),
    user: setupUser(),
  }
}

function rowText(): string[] {
  const table = screen.getByRole('table', { name: 'Recently played' })
  return within(table).getAllByRole('row').slice(1).map(row => row.textContent ?? '')
}

describe('the play history page', () => {
  it('shows each item with its device, cover, source, time and playing length, newest first', async () => {
    renderHistory()
    await screen.findByText('Two')
    expect(rowText()[0]).toContain('Two')
    expect(rowText()[0]).toContain('Bedroom')
    expect(rowText()[0]).toContain('AirPlay')
    expect(rowText()[0]).toContain('2:02')
    expect(rowText()[1]).toContain('One')
    expect(rowText()[1]).toContain('Artist · Album')
    expect(rowText()[1]).toContain('Music · YouTube Music')
    expect(rowText()[1]).toContain('1:05')
    expect(rowText()[1]).toContain('16 Nov, 16:00')
    const images = document.querySelectorAll('img')
    expect(images[0]?.getAttribute('src')).toBe(`/api/v1/devices/${BEDROOM}/artwork?h=${'a'.repeat(64)}`)
    expect(images[1]?.getAttribute('src')).toBe('https://img.example/one.jpg')
  })

  it('filters on the server by device', async () => {
    const { server, user } = renderHistory()
    await screen.findByText('Two')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Device' }), KITCHEN)
    await waitFor(() => expect(rowText()).toHaveLength(1))
    expect(rowText()[0]).toContain('One')
    expect(queryOf(server.requests.filter(request => request.url.includes('/history?')).at(-1)!).deviceId).toBe(KITCHEN)
  })

  it('asks for a longer page when the first is full', async () => {
    const many = Array.from({ length: 50 }, (_, index) => ({ ...ONE, id: index + 1, title: `Track ${index}` }))
    const { server, user } = renderHistory({
      'GET /history': historyRoute(many),
    })
    await screen.findByText('Track 0')
    await user.click(screen.getByRole('button', { name: 'Show more history' }))
    await waitFor(() => expect(queryOf(server.requests.filter(request => request.url.includes('/history?')).at(-1)!).limit).toBe('100'))
  })

  it('rates a song and shows durable favorites', async () => {
    let sent = ''
    const { server, user } = renderHistory({
      'PUT /music/songs/ytmusic/5qZQEq_C3vc/feedback': async (request) => {
        const body = await request.json() as { feedback: string }
        sent = body.feedback
        return Response.json({
          provider: 'ytmusic',
          providerSongId: '5qZQEq_C3vc',
          title: 'One',
          feedback: body.feedback,
          favorite: body.feedback === 'like',
          updatedAt: ONE.startedAt,
          providerSync: 'updated',
        })
      },
      'GET /music/favorites': json({
        items: [
          {
            provider: 'ytmusic',
            providerSongId: '5qZQEq_C3vc',
            title: 'One',
            artist: 'Artist',
            feedback: 'like',
            favorite: true,
            updatedAt: ONE.startedAt,
            webUrl: ONE.webUrl,
          },
        ],
      }),
    })
    expect((await screen.findByRole('link', { name: 'One' })).getAttribute('href')).toBe(ONE.webUrl)
    await user.click(screen.getByRole('button', { name: 'Like One' }))
    await waitFor(() => expect(server.requests.some(request => request.method === 'PUT' && request.url.includes('/feedback'))).toBe(true))
    const put = server.requests.find(request => request.method === 'PUT' && request.url.includes('/feedback'))
    expect(put).toBeDefined()
    expect(sent).toBe('like')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Show' }), 'favorites')
    const table = await screen.findByRole('table', { name: 'Favorite songs' })
    expect(within(table).getByText('One')).toBeTruthy()
    expect(within(table).queryByRole('columnheader', { name: 'Played' })).toBeNull()
  })

  it('has no accessibility violations axe can see without layout', async () => {
    renderHistory()
    await screen.findByText('Two')
    const results = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
      },
    })
    expect(results.violations.map(violation => violation.id)).toEqual([])
  })
})

describe('a device\'s recently played card', () => {
  it('shows the device\'s recent items without another device column', async () => {
    renderHistory({}, () => <RecentlyPlayed deviceId={KITCHEN} />)
    const card = await screen.findByRole('region', { name: 'Recently played' })
    expect(await within(card).findByText('One')).toBeTruthy()
    expect(within(card).queryByText('Two')).toBeNull()
    expect(within(card).queryByRole('columnheader', { name: 'Device' })).toBeNull()
    expect(within(card).getByRole('link', { name: 'View all' }).getAttribute('href')).toBe('/history')
  })
})
