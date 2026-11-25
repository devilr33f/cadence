import type { UserEvent } from '@testing-library/user-event'
// @vitest-environment jsdom
import type { TokenView } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { holdAnimations } from '@/test/animations'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { TokensPage } from './tokens-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  localStorage.clear()
  sessionStorage.clear()
})

// a field's message, which it names among what describes it
function messageOf(field: HTMLElement): string | null | undefined {
  const id = `${field.id}-error`
  expect(field.getAttribute('aria-describedby')?.split(' ')).toContain(id)
  return document.getElementById(id)?.textContent
}

const TOKEN = 'cdn_4f1c0e9b7a2d4c6e8f0a1b3c5d7e9f21'

const OWNER: TokenView = {
  name: 'owner',
  scopes: ['admin'],
  createdAt: '2026-11-10T08:15:00Z',
  lastUsedAt: '2026-11-12T15:04:00Z',
}
const HA: TokenView = {
  name: 'home-assistant',
  scopes: ['ha', 'metrics'],
  createdAt: '2026-11-11T21:40:00Z',
}

// what the token list answers, changed by the test's create and delete routes
function tokens(initial: TokenView[]) {
  const state = { list: initial }
  return {
    state,
    route: (() => Response.json(state.list)) satisfies FakeRoute,
  }
}

function renderTokens(routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: '/tokens',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /tokens': json([OWNER, HA]),
      ...routes,
    },
    pages: { '/tokens': TokensPage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

function rows(): string[][] {
  const table = screen.getByRole('table', { name: 'API tokens' })
  return within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').slice(0, 4).map(cell => cell.textContent ?? ''))
}

function createButton(): HTMLElement {
  return screen.getAllByRole('button', { name: 'Create a token' })[0]!
}

async function openCreate(user: UserEvent) {
  await user.click((await screen.findAllByRole('button', { name: 'Create a token' }))[0]!)
  return screen.findByRole('dialog', { name: 'Create a token' })
}

describe('the tokens list', () => {
  it('shows each token\'s scopes, creation and last use, by name', async () => {
    renderTokens()
    await screen.findByText('owner')
    await waitFor(() => expect(rows()[0]?.[2]).toBe('12 Nov, 03:40'))
    expect(rows()).toEqual([
      [
        'home-assistant',
        'hametrics',
        '12 Nov, 03:40',
        'Never',
      ],
      [
        'owner',
        'admin',
        '10 Nov, 14:15',
        '12 Nov, 21:04',
      ],
    ])
    const scopes = within(screen.getByRole('row', { name: /home-assistant/ })).getByRole('list', { name: 'Scopes' })
    expect(within(scopes).getAllByRole('listitem').map(i => i.textContent)).toEqual(['ha', 'metrics'])
  })

  it('offers to create one when there is none', async () => {
    const { user } = renderTokens({
      'GET /tokens': json([]),
    })
    expect(await screen.findByText('No API tokens')).toBeTruthy()
    const buttons = screen.getAllByRole('button', { name: 'Create a token' })
    expect(buttons).toHaveLength(2)
    await user.click(buttons[1]!)
    expect(await screen.findByRole('dialog', { name: 'Create a token' })).toBeTruthy()
  })

  it('has no accessibility violations axe can see without layout, with the dialog open too', async () => {
    const { user } = renderTokens()
    await screen.findByText('owner')
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
    const rules = {
      'color-contrast': { enabled: false },
    }
    const violations = async () => (await axe.run(document.body, { rules })).violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)
    expect(await violations()).toEqual([])
    await openCreate(user)
    expect(await violations()).toEqual([])
  })
})

describe('creating a token', () => {
  it('sends its name and scopes, shows the token once, and drops it on Done', async () => {
    const list = tokens([OWNER])
    const { server, queryClient, user } = renderTokens({
      'GET /tokens': list.route,
      'POST /tokens': () => {
        list.state.list = [
          OWNER,
          {
            name: 'scripts',
            scopes: ['admin', 'ha'],
            createdAt: '2026-11-12T16:00:00Z',
          },
        ]
        return Response.json({
          name: 'scripts',
          scopes: ['admin', 'ha'],
          token: TOKEN,
        }, {
          status: 201,
          headers: { 'Cache-Control': 'no-store' },
        })
      },
    })
    const dialog = await openCreate(user)
    const name = within(dialog).getByRole('textbox', { name: 'Name (required)' })
    await expectFocusOn(name)
    expect((within(dialog).getByRole('checkbox', { name: 'admin' }) as HTMLInputElement).checked).toBe(true)
    expect((within(dialog).getByRole('checkbox', { name: 'ha' }) as HTMLInputElement).checked).toBe(false)
    expect((within(dialog).getByRole('checkbox', { name: 'metrics' }) as HTMLInputElement).checked).toBe(false)
    await user.type(name, 'scripts')
    await user.click(within(dialog).getByRole('checkbox', { name: 'ha' }))
    await user.click(within(dialog).getByRole('button', { name: 'Create the token' }))

    const box = await within(dialog).findByRole('figure', { name: 'Token for scripts' })
    expect(within(box).getByText(TOKEN)).toBeTruthy()
    expect(within(dialog).getByText(/won't be shown again/)).toBeTruthy()
    await expectFocusOn(within(dialog).getByRole('button', { name: 'Copy the token' }))
    const post = server.requests.find(r => r.method === 'POST')!
    expect(post.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(await post.clone().json()).toEqual({
      name: 'scripts',
      scopes: ['admin', 'ha'],
    })
    // the list behind the dialog already has it, without its value
    expect(await screen.findByText('scripts', { selector: 'td *, td' })).toBeTruthy()

    await user.click(within(dialog).getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(document.body.innerHTML).not.toContain(TOKEN)
    const cached = queryClient.getQueryCache().getAll().map(q => JSON.stringify(q.state.data) ?? '')
    expect(cached.join('')).not.toContain(TOKEN)
    expect(queryClient.getMutationCache().getAll()).toHaveLength(0)
    expect(JSON.stringify({ ...localStorage })).not.toContain(TOKEN)
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(TOKEN)
    expect(window.location.href).not.toContain(TOKEN)
    await expectFocusOn(createButton())

    // opened again, it starts from an empty form
    const again = await openCreate(user)
    expect(within(again).queryByRole('figure')).toBeNull()
    expect((within(again).getByRole('textbox', { name: 'Name (required)' }) as HTMLInputElement).value).toBe('')
  })

  it('keeps the focus on its submit while the create runs and after a refusal beside the form', async () => {
    let answer!: (response: Response) => void
    const { server, user } = renderTokens({
      'POST /tokens': () => new Promise<Response>((resolve) => {
        answer = resolve
      }),
    })
    const dialog = await openCreate(user)
    await user.type(within(dialog).getByRole('textbox', { name: 'Name (required)' }), 'scripts')
    const submit = within(dialog).getByRole('button', { name: 'Create the token' })
    await user.click(submit)
    expect(submit.getAttribute('aria-disabled')).toBe('true')
    expect((submit as HTMLButtonElement).disabled).toBe(false)
    await user.click(submit)
    answer(Response.json({ error: 'unavailable' }, { status: 503 }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server answered 503 unavailable.')
    expect(document.activeElement).toBe(submit)
    expect(submit.getAttribute('aria-disabled')).toBeNull()
    expect(server.calls('POST /tokens')).toBe(1)
  })

  it('drops the token the moment the dialog starts closing, so a reopen during its fade starts empty', async () => {
    const { user } = renderTokens({
      'POST /tokens': json({
        name: 'scripts',
        scopes: ['admin'],
        token: TOKEN,
      }, 201),
    })
    const dialog = await openCreate(user)
    await user.type(within(dialog).getByRole('textbox', { name: 'Name (required)' }), 'scripts')
    await user.click(within(dialog).getByRole('button', { name: 'Create the token' }))
    await within(dialog).findByRole('figure', { name: 'Token for scripts' })

    const fade = holdAnimations()
    await user.click(within(dialog).getByRole('button', { name: 'Done' }))
    expect(document.body.innerHTML).not.toContain(TOKEN)
    await user.click(createButton())
    const again = screen.getByRole('dialog', { name: 'Create a token' })
    expect(within(again).queryByRole('figure')).toBeNull()
    expect((within(again).getByRole('textbox', { name: 'Name (required)' }) as HTMLInputElement).value).toBe('')
    await fade.release()
  })

  it('keeps the token against a click outside and Escape: only Done closes it', async () => {
    const { user } = renderTokens({
      'POST /tokens': json({
        name: 'scripts',
        scopes: ['admin'],
        token: TOKEN,
      }, 201),
    })
    const dialog = await openCreate(user)
    expect(within(dialog).getByRole('button', { name: 'Close' })).toBeTruthy()
    await user.type(within(dialog).getByRole('textbox', { name: 'Name (required)' }), 'scripts')
    await user.click(within(dialog).getByRole('button', { name: 'Create the token' }))
    await within(dialog).findByRole('figure', { name: 'Token for scripts' })
    expect(within(dialog).queryByRole('button', { name: 'Close' })).toBeNull()

    await user.click(document.querySelector('[data-slot="dialog-overlay"]')!)
    await user.keyboard('{Escape}')
    expect(dialog.isConnected).toBe(true)
    expect(within(dialog).getByText(TOKEN)).toBeTruthy()
    await user.click(within(dialog).getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
  })

  it('reads the list again when a create fails, so a token the server made before its answer was lost shows', async () => {
    const list = tokens([OWNER])
    const { user } = renderTokens({
      'GET /tokens': list.route,
      'POST /tokens': () => {
        list.state.list = [
          OWNER,
          {
            name: 'scripts',
            scopes: ['admin'],
            createdAt: '2026-11-12T16:00:00Z',
          },
        ]
        throw new TypeError('Failed to fetch')
      },
    })
    const dialog = await openCreate(user)
    await user.type(within(dialog).getByRole('textbox', { name: 'Name (required)' }), 'scripts')
    await user.click(within(dialog).getByRole('button', { name: 'Create the token' }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server could not be reached.')
    expect(await screen.findByText('scripts', { selector: 'td *, td' })).toBeTruthy()
  })

  it('needs at least one scope, and focuses the first', async () => {
    const { server, user } = renderTokens()
    const dialog = await openCreate(user)
    await user.type(within(dialog).getByRole('textbox', { name: 'Name (required)' }), 'scripts')
    await user.click(within(dialog).getByRole('checkbox', { name: 'admin' }))
    await user.click(within(dialog).getByRole('button', { name: 'Create the token' }))
    const group = within(dialog).getByRole('group', { name: 'Scopes (required)' })
    expect(within(group).getByRole('alert').textContent).toBe('Choose at least one scope.')
    expect(document.activeElement).toBe(within(group).getByRole('checkbox', { name: 'admin' }))
    expect(server.requests.some(r => r.method === 'POST')).toBe(false)
  })

  it('names what is wrong with the name before anything is sent', async () => {
    const { server, user } = renderTokens()
    const dialog = await openCreate(user)
    const name = within(dialog).getByRole('textbox', { name: 'Name (required)' })
    await user.click(within(dialog).getByRole('button', { name: 'Create the token' }))
    expect(messageOf(name)).toBe('Required.')
    await user.type(name, 'home-Assistant')
    await user.click(within(dialog).getByRole('button', { name: 'Create the token' }))
    expect(messageOf(name)).toBe('Lowercase letters, digits, ".", "_" or "-", starting with a letter or a digit; at most 64.')
    expect(document.activeElement).toBe(name)
    expect(server.requests.some(r => r.method === 'POST')).toBe(false)
  })

  it.each([
    [
      { error: 'name_taken' },
      409,
      'The server answered 409 name_taken.',
    ],
    [
      { error: 'invalid', path: '/body/name' },
      422,
      'The server refused this value (invalid).',
    ],
  ])('puts the server\'s %j on the name', async (body, status, text) => {
    const { user } = renderTokens({
      'POST /tokens': json(body, status),
    })
    const dialog = await openCreate(user)
    const name = within(dialog).getByRole('textbox', { name: 'Name (required)' })
    await user.type(name, 'owner')
    await user.click(within(dialog).getByRole('button', { name: 'Create the token' }))
    await waitFor(() => expect(name.getAttribute('aria-invalid')).toBe('true'))
    expect(messageOf(name)).toBe(text)
    await expectFocusOn(name)
    expect(within(dialog).queryByRole('figure')).toBeNull()
  })

  it('gives the focus back to its button when it is cancelled', async () => {
    const { user } = renderTokens()
    const dialog = await openCreate(user)
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    await expectFocusOn(createButton())
  })
})

describe('revoking a token', () => {
  it('asks first, then deletes it and gives the focus to the page\'s action', async () => {
    const list = tokens([OWNER, HA])
    const { server, user } = renderTokens({
      'GET /tokens': list.route,
      'DELETE /tokens/home-assistant': () => {
        list.state.list = [OWNER]
        return new Response(null, { status: 204 })
      },
    })
    await user.click(await screen.findByRole('button', { name: 'Revoke home-assistant' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Revoke home-assistant?' })
    await user.click(within(dialog).getByRole('button', { name: 'Revoke the token' }))
    await waitFor(() => expect(screen.queryByText('home-assistant')).toBeNull())
    expect(server.calls('DELETE /tokens/home-assistant')).toBe(1)
    await waitFor(() => expect(document.activeElement).toBe(createButton()))
  })

  it('shows a refusal inside the dialog, which stays open', async () => {
    const { user } = renderTokens({
      'DELETE /tokens/owner': json({ error: 'not_found' }, 404),
    })
    await user.click(await screen.findByRole('button', { name: 'Revoke owner' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Revoke owner?' })
    await user.click(within(dialog).getByRole('button', { name: 'Revoke the token' }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server answered 404 not_found.')
    expect(dialog.isConnected).toBe(true)
  })
})
