// @vitest-environment jsdom
import type { SkillView } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { enter, setupUser } from '@/test/typing'
import { SkillsPage } from './skills-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const TV: SkillView = {
  name: 'tv',
  description: 'Films, series and anime on a TV screen.',
  body: '- Never invent a ref.',
  tools: ['catalog_search'],
  triggers: ['аниме', 'сериал'],
  modes: null,
  enabled: true,
  source: 'builtin',
  overrides: false,
}

const COFFEE: SkillView = {
  name: 'coffee',
  description: 'The kitchen coffee machine.',
  body: 'Call ha_control.',
  tools: ['ha_control'],
  triggers: ['кофе'],
  modes: ['smart'],
  enabled: false,
  source: 'owner',
  overrides: false,
  updatedAt: '2026-11-15T10:00:00Z',
}

function renderSkills(routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: '/skills',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /skills': json([COFFEE, TV]),
      ...routes,
    },
    pages: { '/skills': SkillsPage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

function rows(): string[][] {
  const table = screen.getByRole('table', { name: 'Skills' })
  return within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').slice(0, 6).map(cell => cell.textContent ?? ''))
}

describe('the skills list', () => {
  it('shows each skill\'s state, source, description, triggers and modes, by name', async () => {
    renderSkills()
    await screen.findByText('tv')
    expect(rows()).toEqual([
      [
        'coffee',
        'Off',
        'Yours',
        'The kitchen coffee machine.',
        '1',
        'smart',
      ],
      [
        'tv',
        'On',
        'Built-in',
        'Films, series and anime on a TV screen.',
        '2',
        'All',
      ],
    ])
    // a built-in is customized, never deleted
    expect(screen.getByRole('button', { name: 'Customize tv' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Delete tv' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Delete coffee' })).toBeTruthy()
  })

  it('stores the owner\'s copy of a built-in, switched off', async () => {
    let body: unknown
    const { user } = renderSkills({
      'PUT /skills/tv': async (req) => {
        body = await req.json()
        return Response.json({
          ...TV,
          source: 'owner',
          overrides: true,
          enabled: false,
        })
      },
    })
    await user.click(await screen.findByRole('button', { name: 'Customize tv' }))
    const dialog = await screen.findByRole('dialog', { name: 'Customize tv' })
    await user.click(within(dialog).getByRole('switch', { name: 'On' }))
    await user.click(within(dialog).getByRole('button', { name: 'Save the skill' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(body).toEqual({
      description: TV.description,
      body: TV.body,
      enabled: false,
      triggers: TV.triggers,
      tools: TV.tools,
      modes: null,
    })
  })

  it('refuses a new skill without its text before sending, and a name the list has', async () => {
    const { user, server } = renderSkills()
    await screen.findByText('tv')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add a skill' }).getAttribute('aria-disabled')).toBeNull())
    await user.click(screen.getByRole('button', { name: 'Add a skill' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a skill' })
    await enter(user, within(dialog).getByRole('textbox', { name: /^Name/ }), 'tv')
    await user.click(within(dialog).getByRole('button', { name: 'Add the skill' }))
    expect(await within(dialog).findByText('A skill has this name: edit it instead.')).toBeTruthy()
    expect(within(dialog).getAllByText('Required.')).toHaveLength(2)
    expect(server.requests.some(r => r.method === 'PUT')).toBe(false)
  })
})
