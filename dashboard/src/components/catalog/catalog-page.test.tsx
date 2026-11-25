// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { CatalogPage } from './catalog-page'

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

function renderCatalog(routes: Record<string, FakeRoute>) {
  const app = renderApp({
    path: '/catalog',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      ...routes,
    },
    pages: { '/catalog': CatalogPage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

const PROVIDERS = {
  providers: [
    {
      id: 'torrserver',
      instance: 'home-ts',
      capabilities: ['search', 'browse', 'resolve'],
      sources: [],
    },
    {
      id: 'torrplayer',
      instance: 'mac-tp',
      capabilities: ['search'],
      failure: 'unreachable',
      sources: [
        {
          provider: 'torrplayer',
          source: 'aniliberty',
          name: 'AniLiberty',
          enabled: true,
        },
        {
          provider: 'torrplayer',
          source: 'nyaa',
          name: 'Nyaa',
          enabled: false,
          torrents: true,
        },
      ],
    },
  ],
}

describe('the catalog page', () => {
  it('says there are no sources yet, with the ways to add one', async () => {
    renderCatalog({
      'GET /catalog/sources': json({ providers: [] }),
    })
    const empty = (await screen.findByText('No catalog sources yet')).parentElement!
    expect(within(empty).getByRole('link', { name: 'Manage the providers' }).getAttribute('href')).toBe('/providers')
    expect(within(empty).getByRole('link', { name: 'Choose the catalog sources' }).getAttribute('href')).toBe('/settings')
    expect(screen.queryByRole('search')).toBeNull()
  })

  it('lists each provider with its sources and a failure by its code', async () => {
    renderCatalog({
      'GET /catalog/sources': json(PROVIDERS),
    })
    const ts = await screen.findByRole('region', { name: 'home-ts' })
    expect(ts.textContent).toContain('TorrServer')
    expect(ts.textContent).toContain('Answering')
    const tp = screen.getByRole('region', { name: 'mac-tp' })
    expect(tp.textContent).toContain('unreachable')
    const sources = within(tp).getByRole('list', { name: 'Sources of mac-tp' })
    expect(within(sources).getAllByRole('listitem').map(li => li.textContent)).toEqual(['AniLibertyOn', 'NyaaOfftorrents'])
  })

  it('tests a search: each provider\'s and source\'s items, or its failure', async () => {
    const asked: string[] = []
    const { user } = renderCatalog({
      'GET /catalog/sources': json(PROVIDERS),
      'GET /catalog/search': (request) => {
        asked.push(new URL(request.url).searchParams.get('q') ?? '')
        return Response.json({
          events: [
            {
              pending: [
                { provider: 'torrserver', name: 'home-ts', enabled: true },
              ],
            },
            {
              provider: 'torrserver',
              items: [
                {
                  kind: 'movie',
                  title: 'Paprika',
                  provider: 'torrserver',
                  playable: true,
                  hasChildren: false,
                  show: { year: 2006 },
                },
              ],
            },
            {
              provider: 'torrplayer',
              source: 'aniliberty',
              failure: { code: 'timeout' },
            },
            { done: true },
          ],
        })
      },
    })
    await user.type(await screen.findByRole('textbox', { name: 'Search the catalog' }), 'paprika')
    await user.click(screen.getByRole('button', { name: 'Search' }))
    const results = await screen.findByRole('status', { name: 'Search results' })
    expect(within(results).getByRole('group', { name: 'TorrServer' }).textContent).toContain('Paprika (2006)movie')
    expect(within(results).getByRole('group', { name: 'torrplayer · aniliberty' }).textContent).toContain('Failed (timeout)')
    expect(asked).toEqual(['paprika'])
  })
})
