import type { UserEvent } from '@testing-library/user-event'
// @vitest-environment jsdom
import type { ModeView } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { enter, replace, setupUser } from '@/test/typing'
import { ModesPage } from './modes-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const FAST: ModeView = {
  name: 'fast',
  model: 'deepseek/deepseek-v4.1-flash-1017',
  reasoning: 'none',
  maxSteps: 8,
  maxSeconds: 20,
  interim: 'drop',
  tools: null,
  aliases: {
    en: ['fast', 'quick'],
    ru: ['быстрый'],
  },
}

const SMART: ModeView = {
  name: 'smart',
  model: '',
  reasoning: 'medium',
  maxSteps: 25,
  maxSeconds: 90,
  maxTokens: 8192,
  firstTokenTimeoutMs: 30000,
  interim: 'speak',
  tools: ['weather', 'web_search'],
  aliases: {
    en: ['smart'],
  },
}

const QUIET: ModeView = {
  name: 'quiet',
  model: '',
  reasoning: 'low',
  maxSteps: 1,
  maxSeconds: 5,
  interim: 'drop',
  tools: [],
  aliases: {},
}

// what the list answers, changed by the test's put and delete routes
function modes(initial: ModeView[]) {
  const state = { list: initial }
  return {
    state,
    route: (() => Response.json(state.list)) satisfies FakeRoute,
  }
}

function renderModes(routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: '/modes',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /modes': json([FAST, QUIET, SMART]),
      ...routes,
    },
    pages: {
      '/modes': ModesPage,
      '/settings': () => <h1>Settings</h1>,
    },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

// a field's message, which it names among what describes it
function messageOf(field: HTMLElement): string | null | undefined {
  const id = `${field.id}-error`
  expect(field.getAttribute('aria-describedby')?.split(' ')).toContain(id)
  return document.getElementById(id)?.textContent
}

function rows(): string[][] {
  const table = screen.getByRole('table', { name: 'Modes' })
  return within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').slice(0, 9).map(cell => cell.textContent ?? ''))
}

function addButton(): HTMLElement {
  return screen.getAllByRole('button', { name: 'Add a mode' })[0]!
}

async function openCreate(user: UserEvent) {
  await waitFor(() => expect(addButton().getAttribute('aria-disabled')).toBeNull())
  await user.click(addButton())
  return screen.findByRole('dialog', { name: 'Add a mode' })
}

function field(dialog: HTMLElement, name: string): HTMLInputElement {
  return within(dialog).getByRole('textbox', { name }) as HTMLInputElement
}

async function addTag(user: UserEvent, dialog: HTMLElement, label: string, text: string) {
  await enter(user, field(dialog, label), text)
  await user.click(within(dialog).getByRole('button', { name: `Add to ${label}` }))
}

async function bodyOf(server: ReturnType<typeof renderApp>['server'], method: string) {
  return server.requests.find(r => r.method === method)!.clone().json()
}

describe('the modes list', () => {
  it('shows each mode\'s model, reasoning, budgets, interim text, tools and aliases, by name', async () => {
    renderModes()
    await screen.findByText('quiet')
    expect(rows()).toEqual([
      [
        'fast',
        'deepseek/deepseek-v4.1-flash-1017',
        'None',
        '8',
        '20',
        'Default',
        'Dropped',
        'All',
        'enfast, quickruбыстрый',
      ],
      [
        'quiet',
        'Device default',
        'Low',
        '1',
        '5',
        'Default',
        'Dropped',
        'None',
        '—',
      ],
      [
        'smart',
        'Device default',
        'Medium',
        '25',
        '90',
        '8192',
        'Spoken',
        '2 tools',
        'ensmart',
      ],
    ])
  })

  it('links the settings that choose the modes', async () => {
    renderModes()
    await screen.findByText('quiet')
    expect(screen.getByRole('link', { name: 'Open the mode setting' }).getAttribute('href')).toBe('/settings#setting-mode')
    expect(screen.getByRole('link', { name: 'Open the allowedModes setting' }).getAttribute('href')).toBe('/settings#setting-allowedModes')
  })

  it('teaches what a mode is when there is none', async () => {
    renderModes({
      'GET /modes': json([]),
    })
    expect(await screen.findByText('No modes')).toBeTruthy()
    expect(screen.getAllByRole('button', { name: 'Add a mode' })).toHaveLength(2)
  })

  it('says why the list could not be read, in codes', async () => {
    renderModes({
      'GET /modes': json({ error: 'forbidden' }, 403),
    })
    expect(await screen.findByText('The server answered 403 forbidden.')).toBeTruthy()
  })

  it('has no accessibility violations axe can see without layout, with the dialog open too', async () => {
    const { user } = renderModes()
    await screen.findByText('quiet')
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
    const rules = {
      'color-contrast': { enabled: false },
    }
    const violations = async () => (await axe.run(document.body, { rules })).violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)
    expect(await violations()).toEqual([])
    const dialog = await openCreate(user)
    await user.click(within(dialog).getByRole('switch', { name: 'All tools' }))
    expect(await violations()).toEqual([])
  })
})

describe('adding a mode', () => {
  // the names taken come from the list: before it is read, a create of an existing name would replace it
  it('holds Add until the list is read', async () => {
    let answer!: () => void
    const { user } = renderModes({
      'GET /modes': () => new Promise<Response>((resolve) => {
        answer = () => resolve(Response.json([FAST, QUIET, SMART]))
      }),
    })
    await waitFor(() => expect(addButton().getAttribute('aria-disabled')).toBe('true'))
    await user.click(addButton())
    expect(screen.queryByRole('dialog')).toBeNull()
    answer()
    expect(await openCreate(user)).toBeTruthy()
  })

  it('holds Add while the list could not be read', async () => {
    const { user } = renderModes({
      'GET /modes': json({ error: 'forbidden' }, 403),
    })
    expect((await screen.findByRole('alert')).textContent).toContain('The server answered 403 forbidden.')
    await user.click(addButton())
    expect(addButton().getAttribute('aria-disabled')).toBe('true')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('puts every field, the chosen tools and both languages\' aliases, then refreshes the list and gives the focus back', async () => {
    const list = modes([FAST])
    const { server, user } = renderModes({
      'GET /modes': list.route,
      'PUT /modes/night': async (request) => {
        const body = await request.clone().json() as Omit<ModeView, 'name'>
        list.state.list = [
          ...list.state.list,
          { name: 'night', ...body },
        ]
        return Response.json(list.state.list.at(-1))
      },
    })
    const dialog = await openCreate(user)
    await expectFocusOn(field(dialog, 'Name (required)'))
    await enter(user, field(dialog, 'Name (required)'), 'night')
    await enter(user, field(dialog, 'Model'), 'openai/gpt-5-mini')
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Reasoning (required)' }), 'low')
    await replace(user, field(dialog, 'Steps (required)'), '12')
    await replace(user, field(dialog, 'Seconds (required)'), '45')
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Interim text (required)' }), 'speak')
    const all = within(dialog).getByRole('switch', { name: 'All tools' })
    expect(all.getAttribute('aria-checked')).toBe('true')
    expect(within(dialog).queryByRole('textbox', { name: 'Tools' })).toBeNull()
    await user.click(all)
    await addTag(user, dialog, 'Tools', 'weather')
    await addTag(user, dialog, 'English aliases', ' night ')
    // Enter adds the item, as the Add button does
    await enter(user, field(dialog, 'Russian aliases'), 'ночной')
    await user.keyboard('{Enter}')
    expect(dialog.isConnected).toBe(true)
    await user.click(within(dialog).getByRole('button', { name: 'Add the mode' }))

    await waitFor(() => expect(dialog.isConnected).toBe(false))
    const put = server.requests.find(r => r.method === 'PUT')!
    expect(put.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(await put.clone().json()).toEqual({
      model: 'openai/gpt-5-mini',
      reasoning: 'low',
      maxSteps: 12,
      maxSeconds: 45,
      interim: 'speak',
      tools: ['weather'],
      aliases: {
        en: ['night'],
        ru: ['ночной'],
      },
    })
    await waitFor(() => expect(rows().map(r => r[0])).toEqual(['fast', 'night']))
    await expectFocusOn(addButton())
  })

  it('sends null for every tool and the defaults it opens with', async () => {
    const { server, user } = renderModes({
      'PUT /modes/plain': json({ ...QUIET, name: 'plain' }),
    })
    const dialog = await openCreate(user)
    await enter(user, field(dialog, 'Name (required)'), 'plain')
    await user.click(within(dialog).getByRole('button', { name: 'Add the mode' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await bodyOf(server, 'PUT')).toEqual({
      model: '',
      reasoning: 'none',
      maxSteps: 8,
      maxSeconds: 20,
      interim: 'drop',
      tools: null,
      aliases: {},
    })
  })

  it('sends a token ceiling the owner typed, which the list then shows', async () => {
    const list = modes([FAST])
    const { server, user } = renderModes({
      'GET /modes': list.route,
      'PUT /modes/deep': async (request) => {
        const body = await request.clone().json() as Omit<ModeView, 'name'>
        list.state.list = [
          ...list.state.list,
          { name: 'deep', ...body },
        ]
        return Response.json(list.state.list.at(-1))
      },
    })
    const dialog = await openCreate(user)
    await enter(user, field(dialog, 'Name (required)'), 'deep')
    await replace(user, field(dialog, 'Max tokens'), '4096')
    await user.click(within(dialog).getByRole('button', { name: 'Add the mode' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await bodyOf(server, 'PUT')).toMatchObject({ maxTokens: 4096 })
    // the list orders by name, so deep comes before fast
    await waitFor(() => expect(rows().map(r => r[0])).toEqual(['deep', 'fast']))
    expect(rows()[0]![5]).toBe('4096')
  })

  it('sends an empty list when every tool is switched off and none is named', async () => {
    const { server, user } = renderModes({
      'PUT /modes/bare': json({ ...QUIET, name: 'bare' }),
    })
    const dialog = await openCreate(user)
    await enter(user, field(dialog, 'Name (required)'), 'bare')
    await user.click(within(dialog).getByRole('switch', { name: 'All tools' }))
    await user.click(within(dialog).getByRole('button', { name: 'Add the mode' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect((await bodyOf(server, 'PUT')).tools).toEqual([])
  })

  it('names what the form needs before anything is sent, and focuses the first', async () => {
    const { server, user } = renderModes()
    const dialog = await openCreate(user)
    await enter(user, field(dialog, 'Name (required)'), 'Night mode')
    await replace(user, field(dialog, 'Steps (required)'), '51')
    await replace(user, field(dialog, 'Seconds (required)'), '')
    await replace(user, field(dialog, 'Max tokens'), '63')
    await replace(user, field(dialog, 'First token timeout'), '1')
    await enter(user, field(dialog, 'Model'), 'é'.repeat(101))
    await user.click(within(dialog).getByRole('switch', { name: 'All tools' }))
    await enter(user, field(dialog, 'Tools'), 'weather')
    await user.click(within(dialog).getByRole('button', { name: 'Add the mode' }))
    expect(messageOf(field(dialog, 'Name (required)'))).toBe('Lowercase letters, digits, "_" or "-", starting with a letter; at most 32.')
    expect(messageOf(field(dialog, 'Model'))).toBe('At most 200 bytes.')
    expect(messageOf(field(dialog, 'Steps (required)'))).toBe('At most 50.')
    expect(messageOf(field(dialog, 'Seconds (required)'))).toBe('Required.')
    expect(messageOf(field(dialog, 'Max tokens'))).toBe('At least 64.')
    expect(messageOf(field(dialog, 'First token timeout'))).toBe('At least 2.')
    expect(messageOf(field(dialog, 'Tools'))).toBe('Add the typed item, or clear it.')
    await expectFocusOn(field(dialog, 'Name (required)'))
    expect(server.requests.some(r => r.method === 'PUT')).toBe(false)
  })

  it('refuses a name a mode already has, so a create never replaces it', async () => {
    const { server, user } = renderModes()
    const dialog = await openCreate(user)
    await enter(user, field(dialog, 'Name (required)'), 'smart')
    await user.click(within(dialog).getByRole('button', { name: 'Add the mode' }))
    expect(messageOf(field(dialog, 'Name (required)'))).toBe('A mode has this name: edit it instead.')
    expect(server.requests.some(r => r.method === 'PUT')).toBe(false)
  })

  it.each([
    [
      'English aliases',
      'a'.repeat(41),
      'At most 40 characters.',
    ],
    ['English aliases', 'FAST', 'Already in the list.'],
    ['Tools', 'web search', 'Letters, digits, "_" or "-"; at most 64.'],
  ])('holds an item of %s the server would refuse: %s', async (label, text, message) => {
    const { user } = renderModes()
    const dialog = await openCreate(user)
    await user.click(within(dialog).getByRole('switch', { name: 'All tools' }))
    await addTag(user, dialog, 'English aliases', 'fast')
    await addTag(user, dialog, label, text)
    expect(messageOf(field(dialog, label))).toBe(message)
    expect(field(dialog, label).value).toBe(text)
  })
})

describe('the server\'s refusals', () => {
  async function refusedWith(body: object, status: number) {
    const rendered = renderModes({
      'PUT /modes/night': json(body, status),
    })
    const { user } = rendered
    const dialog = await openCreate(user)
    await enter(user, field(dialog, 'Name (required)'), 'night')
    await user.click(within(dialog).getByRole('switch', { name: 'All tools' }))
    await addTag(user, dialog, 'Tools', 'weather')
    await addTag(user, dialog, 'English aliases', 'late')
    await addTag(user, dialog, 'English aliases', 'quick')
    await user.click(within(dialog).getByRole('button', { name: 'Add the mode' }))
    return { ...rendered, dialog }
  }

  it.each([
    [
      { error: 'alias_taken', path: '/body/aliases/en/1' },
      409,
      'English aliases',
      '«quick» is another mode\'s name or alias (alias_taken).',
    ],
    [
      { error: 'alias_taken', path: '/path/name' },
      409,
      'Name (required)',
      'Another mode has this as an alias (alias_taken).',
    ],
    [
      { error: 'invalid', path: '/path/name' },
      422,
      'Name (required)',
      'The server refused this value (invalid).',
    ],
    [
      { error: 'invalid', path: '/body/maxSteps' },
      422,
      'Steps (required)',
      'The server refused this value (invalid).',
    ],
    [
      { error: 'invalid', path: '/body/maxTokens' },
      422,
      'Max tokens',
      'The server refused this value (invalid).',
    ],
    [
      { error: 'invalid', path: '/body/firstTokenTimeoutMs' },
      422,
      'First token timeout',
      'The server refused this value (invalid).',
    ],
    [
      { error: 'invalid', path: '/body/tools/0' },
      422,
      'Tools',
      'The server refused «weather» (invalid).',
    ],
    [
      { error: 'invalid', path: '/body/aliases/en' },
      422,
      'English aliases',
      'The server refused this value (invalid).',
    ],
  ])('puts %j on the field it is about, and focuses it', async (body, status, label, message) => {
    const { dialog } = await refusedWith(body, status)
    const input = field(dialog, label)
    await waitFor(() => expect(input.getAttribute('aria-invalid')).toBe('true'))
    expect(messageOf(input)).toBe(message)
    await expectFocusOn(input)
  })

  it('words any other refusal beside the form, in codes, the focus kept on its submit', async () => {
    const { dialog } = await refusedWith({ error: 'internal' }, 500)
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server answered 500 internal.')
    expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Add the mode' }))
  })
})

describe('editing a mode', () => {
  it('opens on what the mode holds, sends it whole under its name, and gives the focus back to its button', async () => {
    const list = modes([FAST, QUIET, SMART])
    const { server, user } = renderModes({
      'GET /modes': list.route,
      'PUT /modes/smart': async (request) => {
        const body = await request.clone().json() as Omit<ModeView, 'name'>
        list.state.list = [
          FAST,
          QUIET,
          {
            ...SMART,
            ...body,
          },
        ]
        return Response.json(list.state.list[2])
      },
    })
    const edit = await screen.findByRole('button', { name: 'Edit smart' })
    await user.click(edit)
    const dialog = await screen.findByRole('dialog', { name: 'Edit smart' })
    expect(within(dialog).queryByRole('textbox', { name: 'Name (required)' })).toBeNull()
    expect(field(dialog, 'Steps (required)').value).toBe('25')
    expect(field(dialog, 'Max tokens').value).toBe('8192')
    expect(field(dialog, 'First token timeout').value).toBe('30')
    expect(within(dialog).getByRole('switch', { name: 'All tools' }).getAttribute('aria-checked')).toBe('false')
    expect(within(within(dialog).getByRole('list', { name: 'Items of Tools' })).getAllByRole('listitem').map(i => i.textContent)).toEqual(['weather', 'web_search'])
    await replace(user, field(dialog, 'Seconds (required)'), '120')
    await user.click(within(dialog).getByRole('button', { name: 'Save the mode' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await bodyOf(server, 'PUT')).toEqual({
      model: '',
      reasoning: 'medium',
      maxSteps: 25,
      maxSeconds: 120,
      maxTokens: 8192,
      firstTokenTimeoutMs: 30000,
      interim: 'speak',
      tools: ['weather', 'web_search'],
      aliases: {
        en: ['smart'],
      },
    })
    expect(await screen.findByText('120')).toBeTruthy()
    await expectFocusOn(screen.getByRole('button', { name: 'Edit smart' }))
  })

  it.each([
    ['fast', 'true'],
    ['quiet', 'false'],
  ])('opens %s with all tools %s, keeping null apart from an empty list', async (name, checked) => {
    const { server, user } = renderModes({
      [`PUT /modes/${name}`]: json(name === 'fast'
        ? FAST
        : QUIET),
    })
    await user.click(await screen.findByRole('button', { name: `Edit ${name}` }))
    const dialog = await screen.findByRole('dialog', { name: `Edit ${name}` })
    expect(within(dialog).getByRole('switch', { name: 'All tools' }).getAttribute('aria-checked')).toBe(checked)
    await user.click(within(dialog).getByRole('button', { name: 'Save the mode' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect((await bodyOf(server, 'PUT')).tools).toEqual(name === 'fast'
      ? null
      : [])
  })
})

describe('deleting a mode', () => {
  it('asks first, then deletes it and gives the focus to the page\'s action', async () => {
    const list = modes([FAST, QUIET])
    const { server, user } = renderModes({
      'GET /modes': list.route,
      'DELETE /modes/quiet': () => {
        list.state.list = [FAST]
        return new Response(null, { status: 204 })
      },
    })
    await user.click(await screen.findByRole('button', { name: 'Delete quiet' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Delete quiet?' })
    await user.click(within(dialog).getByRole('button', { name: 'Delete the mode' }))
    await waitFor(() => expect(screen.queryByText('quiet')).toBeNull())
    expect(server.calls('DELETE /modes/quiet')).toBe(1)
    await expectFocusOn(addButton())
  })

  it('words a mode in use by its code inside the dialog, which stays open', async () => {
    const { server, user } = renderModes({
      'DELETE /modes/fast': json({ error: 'in_use' }, 409),
    })
    await user.click(await screen.findByRole('button', { name: 'Delete fast' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Delete fast?' })
    await user.click(within(dialog).getByRole('button', { name: 'Delete the mode' }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server answered 409 in_use.')
    expect(dialog.isConnected).toBe(true)
    expect(server.calls('GET /modes')).toBe(1)
  })
})
