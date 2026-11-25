import type { UserEvent } from '@testing-library/user-event'
// @vitest-environment jsdom
import type { Instance, ModuleInfo } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { holdAnimations } from '@/test/animations'
import { json, noContent, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { stubColorScheme } from '@/test/media'
import { CATALOG } from '@/test/provider-modules'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { ProvidersPage } from './providers-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

// a field's message, which it names among what describes it
function messageOf(field: HTMLElement): string | null | undefined {
  const id = `${field.id}-error`
  expect(field.getAttribute('aria-describedby')?.split(' ')).toContain(id)
  return document.getElementById(id)?.textContent
}

// a value no page may keep: it goes into the request and nowhere else
const SECRET = 'llat-0b7c51d2e9'

function instance(name: string, module: string, secrets: string[] = [], createdAt = '2026-11-10T08:15:00Z'): Instance {
  return {
    id: `id-${name}`,
    name,
    module,
    config: {},
    createdAt,
    secrets: Object.fromEntries(secrets.map(s => [
      s,
      { set: true },
    ])),
  }
}

// by kind in the page's order (the voice first), a kind newer than the page after them, then by name; a module the
// catalog does not list goes last
const HOUSEHOLD: Instance[] = [
  instance('router', 'openrouter', ['apiKey']),
  instance('sim-stt', 'fake-stt'),
  instance('collector', 'otlp-http'),
  instance('agenda', 'calendar-ical'),
  instance('home', 'homeassistant', ['token']),
  instance('forecast', 'open-meteo'),
  instance('aura', 'deepgram-aura', ['apiKey'], '2026-11-11T21:40:00Z'),
  instance('vectors', 'openai-compatible'),
  instance('flux', 'deepgram-flux', ['apiKey']),
]

function renderProviders(routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: '/providers',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /providers': json(HOUSEHOLD),
      'GET /providers/modules': json(CATALOG),
      ...routes,
    },
    pages: { '/providers': ProvidersPage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

function rows(): string[][] {
  const table = screen.getByRole('table', { name: 'Providers' })
  return within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').slice(0, 5).map(cell => cell.textContent ?? ''))
}

function addButton(): HTMLElement {
  return screen.getAllByRole('button', { name: 'Add a provider' })[0]!
}

// what an instance list answers, changed by the test's create and delete routes
function household(initial: Instance[]) {
  const state = { list: initial }
  return {
    state,
    route: (() => Response.json(state.list)) satisfies FakeRoute,
  }
}

async function openCreate(user: UserEvent) {
  await waitFor(() => expect(addButton().getAttribute('aria-disabled')).toBeNull())
  await user.click(addButton())
  return screen.findByRole('dialog', { name: 'Add a provider' })
}

async function fill(user: UserEvent, dialog: HTMLElement, { name, module }: { name: string, module: string }) {
  await user.type(within(dialog).getByRole('textbox', { name: 'Name (required)' }), name)
  await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Module (required)' }), module)
}

function moduleSelect(dialog: HTMLElement): HTMLSelectElement {
  return within(dialog).getByRole('combobox', { name: 'Module (required)' }) as HTMLSelectElement
}

function noTraceOf(value: string, queryClient: ReturnType<typeof renderApp>['queryClient']) {
  expect(document.body.innerHTML).not.toContain(value)
  expect([
    ...document.querySelectorAll('input, textarea'),
  ].map(f => (f as HTMLInputElement).value)).not.toContain(value)
  const cached = queryClient.getQueryCache().getAll().map(q => JSON.stringify(q.state.data) ?? '')
  expect(cached.join('')).not.toContain(value)
  expect(queryClient.getMutationCache().getAll()).toHaveLength(0)
  expect(JSON.stringify({ ...localStorage })).not.toContain(value)
  expect(JSON.stringify({ ...sessionStorage })).not.toContain(value)
}

describe('the providers list', () => {
  it('shows each instance\'s module, kind and secrets as the catalog has them, and its creation, by kind and name', async () => {
    renderProviders()
    await screen.findByText('flux')
    await waitFor(() => expect(rows()[0]?.[4]).toBe('10 Nov, 14:15'))
    expect(rows()).toEqual([
      [
        'flux',
        'deepgram-flux',
        'Speech to text',
        'apiKeySet',
        '10 Nov, 14:15',
      ],
      [
        'aura',
        'deepgram-aura',
        'Text to speech',
        'apiKeySet',
        '12 Nov, 03:40',
      ],
      [
        'router',
        'openrouter',
        'Language model',
        'apiKeySet',
        '10 Nov, 14:15',
      ],
      [
        'forecast',
        'open-meteo',
        'Weather',
        'None',
        '10 Nov, 14:15',
      ],
      [
        'vectors',
        'openai-compatible',
        'Embeddings',
        'apiKeyNot set',
        '10 Nov, 14:15',
      ],
      [
        'home',
        'homeassistant',
        'Home Assistant',
        'tokenSet',
        '10 Nov, 14:15',
      ],
      [
        'collector',
        'otlp-http',
        'Telemetry export',
        'authorizationNot set',
        '10 Nov, 14:15',
      ],
      [
        'agenda',
        'calendar-ical',
        'calendar',
        'None',
        '10 Nov, 14:15',
      ],
      [
        'sim-stt',
        'fake-stt',
        'Unknown',
        '—',
        '10 Nov, 14:15',
      ],
    ])
  })

  it('teaches what a provider is when there is none, and offers to add one', async () => {
    const { user } = renderProviders({
      'GET /providers': json([]),
    })
    expect(await screen.findByText('No providers yet')).toBeTruthy()
    const buttons = screen.getAllByRole('button', { name: 'Add a provider' })
    expect(buttons).toHaveLength(2)
    await user.click(buttons[1]!)
    expect(await screen.findByRole('dialog', { name: 'Add a provider' })).toBeTruthy()
  })

  it.each([
    ['GET /providers'],
    ['GET /providers/modules'],
  ])('says why the list could not be read when %s is refused, in codes', async (route) => {
    renderProviders({
      [route]: json({ error: 'forbidden' }, 403),
    })
    expect(await screen.findByText('The server answered 403 forbidden.')).toBeTruthy()
    expect(screen.queryByText('flux')).toBeNull()
  })

  it('has no accessibility violations axe can see without layout, with the dialog open too', async () => {
    const { user } = renderProviders()
    await screen.findByText('flux')
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
    const rules = {
      'color-contrast': { enabled: false },
    }
    const violations = async () => (await axe.run(document.body, { rules })).violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)
    expect(await violations()).toEqual([])
    const dialog = await openCreate(user)
    await user.selectOptions(moduleSelect(dialog), 'otlp-http')
    expect(await violations()).toEqual([])
  })
})

describe('adding a provider', () => {
  // its modules come from the catalog: before it is read, the dialog would offer none
  it('holds Add until the catalog is read', async () => {
    let answer!: () => void
    const { user } = renderProviders({
      'GET /providers/modules': () => new Promise<Response>((resolve) => {
        answer = () => resolve(Response.json(CATALOG))
      }),
    })
    await waitFor(() => expect(addButton().getAttribute('aria-disabled')).toBe('true'))
    await user.click(addButton())
    expect(screen.queryByRole('dialog')).toBeNull()
    answer()
    const dialog = await openCreate(user)
    expect(moduleSelect(dialog).options.length).toBeGreaterThan(1)
  })

  it('holds Add while the catalog could not be read', async () => {
    const { user } = renderProviders({
      'GET /providers/modules': json({ error: 'forbidden' }, 403),
    })
    expect((await screen.findByRole('alert')).textContent).toContain('The server answered 403 forbidden.')
    await user.click(addButton())
    expect(addButton().getAttribute('aria-disabled')).toBe('true')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('offers the catalog\'s modules by kind, in the page\'s order, a newer kind last', async () => {
    const { user } = renderProviders()
    const dialog = await openCreate(user)
    const groups = [
      ...moduleSelect(dialog).querySelectorAll('optgroup'),
    ].map(g => [
      g.label,
      [
        ...g.querySelectorAll('option'),
      ].map(o => o.value),
    ])
    expect(groups).toEqual([
      [
        'Speech to text',
        ['deepgram-flux'],
      ],
      [
        'Text to speech',
        ['deepgram-aura'],
      ],
      [
        'Language model',
        ['openrouter'],
      ],
      [
        'Weather',
        ['open-meteo'],
      ],
      [
        'Embeddings',
        ['openai-compatible'],
      ],
      [
        'Home Assistant',
        ['homeassistant'],
      ],
      [
        'Telemetry export',
        ['otlp-http'],
      ],
      [
        'calendar',
        ['calendar-ical'],
      ],
    ])
  })

  it('fills the module\'s config through its schema and its secret, sends them, then leaves no trace of the secret', async () => {
    const providers = household([])
    const { server, queryClient, user } = renderProviders({
      'GET /providers': providers.route,
      'POST /providers': () => {
        providers.state.list = [
          instance('home', 'homeassistant', ['token']),
        ]
        return Response.json(providers.state.list[0], { status: 201 })
      },
    })
    const dialog = await openCreate(user)
    await expectFocusOn(within(dialog).getByRole('textbox', { name: 'Name (required)' }))
    expect(within(dialog).queryByRole('group', { name: 'Configuration' })).toBeNull()
    await fill(user, dialog, { name: 'home', module: 'homeassistant' })
    const config = within(dialog).getByRole('group', { name: 'Configuration' })
    await user.type(within(config).getByRole('textbox', { name: 'baseUrl (required)' }), 'http://ha.local:8123')
    const token = within(dialog).getByRole('textbox', { name: 'token (required)' }) as HTMLInputElement
    await user.type(token, SECRET)
    expect(token.getAttribute('value')).toBeNull()
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))

    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await screen.findByText('home')).toBeTruthy()
    const post = server.requests.find(r => r.method === 'POST')!
    expect(post.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(await post.clone().json()).toEqual({
      name: 'home',
      module: 'homeassistant',
      config: { baseUrl: 'http://ha.local:8123' },
      secrets: { token: SECRET },
    })
    noTraceOf(SECRET, queryClient)
    await expectFocusOn(addButton())
  })

  // the module's author orders its fields, an optional one before a required one too
  it('shows the config\'s fields in the module\'s order, as the catalog sends them', async () => {
    const { user } = renderProviders({
      'GET /providers/modules': json([
        ...CATALOG,
        {
          id: 'caldav',
          kind: 'calendar',
          configSchema: {
            type: 'object',
            additionalProperties: false,
            required: ['url'],
            properties: {
              label: { type: 'string' },
              url: { type: 'string', pattern: '^https://.' },
            },
          },
          secrets: [],
        },
      ]),
    })
    const dialog = await openCreate(user)
    await user.selectOptions(moduleSelect(dialog), 'caldav')
    const config = within(dialog).getByRole('group', { name: 'Configuration' })
    expect(within(config).getAllByRole('textbox')).toEqual([
      within(config).getByRole('textbox', { name: 'label' }),
      within(config).getByRole('textbox', { name: 'url (required)' }),
    ])
  })

  it('says when a module takes no configuration, and sends an empty one', async () => {
    const { server, user } = renderProviders({
      'POST /providers': json(instance('forecast2', 'open-meteo'), 201),
    })
    const dialog = await openCreate(user)
    await fill(user, dialog, { name: 'forecast2', module: 'open-meteo' })
    expect(within(dialog).getByText('This module takes no configuration.')).toBeTruthy()
    expect(within(dialog).queryByText('Secrets')).toBeNull()
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await server.requests.find(r => r.method === 'POST')!.clone().json()).toEqual({
      name: 'forecast2',
      module: 'open-meteo',
      config: {},
    })
  })

  it('offers a field per secret of the chosen module, the required ones marked, and sends no empty optional one', async () => {
    const { server, user } = renderProviders({
      'POST /providers': json(instance('collector2', 'otlp-http'), 201),
    })
    const dialog = await openCreate(user)
    expect(within(dialog).queryByText('Secrets')).toBeNull()
    await fill(user, dialog, { name: 'collector2', module: 'deepgram-flux' })
    expect(within(dialog).getByRole('textbox', { name: 'apiKey (required)' })).toBeTruthy()
    await user.selectOptions(moduleSelect(dialog), 'otlp-http')
    expect(within(dialog).getByRole('textbox', { name: 'authorization' })).toBeTruthy()
    await user.type(within(dialog).getByRole('textbox', { name: 'url (required)' }), 'https://otel.local')
    await user.type(within(dialog).getByRole('textbox', { name: 'timeoutMs' }), '5000')
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await server.requests.find(r => r.method === 'POST')!.clone().json()).toEqual({
      name: 'collector2',
      module: 'otlp-http',
      config: { url: 'https://otel.local', timeoutMs: 5000 },
    })
  })

  it('drops what was typed for one module\'s config and secret when another module is chosen', async () => {
    // the config and the secrets are each keyed by the module: two siblings under one key confuse react's reconciler
    const errors = vi.spyOn(console, 'error')
    const { user } = renderProviders()
    const dialog = await openCreate(user)
    await fill(user, dialog, { name: 'vectors2', module: 'deepgram-flux' })
    await user.type(within(dialog).getByRole('textbox', { name: 'model' }), 'nova-3')
    await user.type(within(dialog).getByRole('textbox', { name: 'apiKey (required)' }), SECRET)
    await user.selectOptions(moduleSelect(dialog), 'openai-compatible')
    expect((within(dialog).getByRole('textbox', { name: 'apiKey' }) as HTMLInputElement).value).toBe('')
    expect((within(dialog).getByRole('textbox', { name: 'model (required)' }) as HTMLInputElement).value).toBe('')
    expect(errors).not.toHaveBeenCalled()
  })

  it('holds a required config field and a required secret left empty, and focuses the first', async () => {
    const { server, user } = renderProviders()
    const dialog = await openCreate(user)
    await fill(user, dialog, { name: 'home2', module: 'homeassistant' })
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))
    const baseUrl = within(dialog).getByRole('textbox', { name: 'baseUrl (required)' })
    const token = within(dialog).getByRole('textbox', { name: 'token (required)' })
    expect(messageOf(baseUrl)).toBe('Required.')
    expect(messageOf(token)).toBe('Required.')
    await expectFocusOn(baseUrl)
    await user.type(baseUrl, 'ftp://ha')
    expect(messageOf(baseUrl)).toBe('Must match ^https?://.')
    expect(server.requests.some(r => r.method === 'POST')).toBe(false)
  })

  it('names what the form needs before anything is sent', async () => {
    const { server, user } = renderProviders()
    const dialog = await openCreate(user)
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))
    expect(within(dialog).getAllByRole('alert').map(a => a.textContent)).toEqual(['Required.', 'Choose a module.'])
    expect(document.activeElement).toBe(within(dialog).getByRole('textbox', { name: 'Name (required)' }))
    await user.type(within(dialog).getByRole('textbox', { name: 'Name (required)' }), 'bad name')
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))
    expect(within(dialog).getAllByRole('alert').map(a => a.textContent)).toEqual([
      'Letters, digits, ".", "_" or "-", starting with a letter or a digit; at most 64.',
      'Choose a module.',
    ])
    const name = within(dialog).getByRole('textbox', { name: 'Name (required)' })
    await user.clear(name)
    await user.type(name, 'Modules')
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))
    expect(messageOf(name)).toBe('Reserved for the module catalog.')
    expect(server.requests.some(r => r.method === 'POST')).toBe(false)
  })

  it('puts the server\'s 422 path on the config field it names, clearing the secrets', async () => {
    const { user } = renderProviders({
      'POST /providers': json({ error: 'config_invalid', path: '/baseUrl' }, 422),
    })
    const dialog = await openCreate(user)
    await fill(user, dialog, { name: 'home', module: 'homeassistant' })
    const baseUrl = within(dialog).getByRole('textbox', { name: 'baseUrl (required)' })
    await user.type(baseUrl, 'http://ha')
    await user.type(within(dialog).getByRole('textbox', { name: 'token (required)' }), SECRET)
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))
    await waitFor(() => expect(baseUrl.getAttribute('aria-invalid')).toBe('true'))
    expect(messageOf(baseUrl)).toBe('The server refused this value (config_invalid).')
    await expectFocusOn(baseUrl)
    expect((within(dialog).getByRole('textbox', { name: 'token (required)' }) as HTMLInputElement).value).toBe('')
    expect(document.body.innerHTML).not.toContain(SECRET)
  })

  it('words a config path no field has on the configuration as a whole', async () => {
    const { user } = renderProviders({
      'POST /providers': json({ error: 'config_invalid', path: '/extra' }, 422),
    })
    const dialog = await openCreate(user)
    await fill(user, dialog, { name: 'home', module: 'homeassistant' })
    const baseUrl = within(dialog).getByRole('textbox', { name: 'baseUrl (required)' })
    await user.type(baseUrl, 'http://ha')
    await user.type(within(dialog).getByRole('textbox', { name: 'token (required)' }), SECRET)
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))
    const config = within(dialog).getByRole('group', { name: 'Configuration' })
    expect((await within(config).findByRole('alert')).textContent).toBe('The server refused the configuration at /extra (config_invalid).')
    await expectFocusOn(baseUrl)
  })

  it.each([
    [
      { error: 'config_invalid', path: '/secrets/token' },
      422,
      'token (required)',
      'The server refused this value (config_invalid).',
    ],
    [
      { error: 'config_invalid', path: '/name' },
      422,
      'Name (required)',
      'The server refused this value (config_invalid).',
    ],
    [
      { error: 'name_taken' },
      409,
      'Name (required)',
      'The server answered 409 name_taken.',
    ],
  ])('puts the server\'s %j on the field it is about', async (body, status, field, text) => {
    const { user } = renderProviders({
      'POST /providers': json(body, status),
    })
    const dialog = await openCreate(user)
    await fill(user, dialog, { name: 'home', module: 'homeassistant' })
    await user.type(within(dialog).getByRole('textbox', { name: 'baseUrl (required)' }), 'http://ha')
    await user.type(within(dialog).getByRole('textbox', { name: 'token (required)' }), SECRET)
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))
    const input = within(dialog).getByRole('textbox', { name: field })
    await waitFor(() => expect(input.getAttribute('aria-invalid')).toBe('true'))
    expect(messageOf(input)).toBe(text)
    await expectFocusOn(input)
  })

  it('keeps the focus on its submit while the create runs and after a refusal beside the form', async () => {
    let answer!: (response: Response) => void
    const { server, user } = renderProviders({
      'POST /providers': () => new Promise<Response>((resolve) => {
        answer = resolve
      }),
    })
    const dialog = await openCreate(user)
    await fill(user, dialog, { name: 'forecast2', module: 'open-meteo' })
    const submit = within(dialog).getByRole('button', { name: 'Add the provider' })
    await user.click(submit)
    expect(submit.getAttribute('aria-disabled')).toBe('true')
    expect((submit as HTMLButtonElement).disabled).toBe(false)
    await user.click(submit)
    answer(Response.json({ error: 'unavailable' }, { status: 503 }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server answered 503 unavailable.')
    expect(document.activeElement).toBe(submit)
    expect(submit.getAttribute('aria-disabled')).toBeNull()
    expect(server.calls('POST /providers')).toBe(1)
  })

  it('drops the typed secret the moment the dialog starts closing, so a reopen during its fade starts empty', async () => {
    const { user } = renderProviders()
    const dialog = await openCreate(user)
    await fill(user, dialog, { name: 'home', module: 'homeassistant' })
    await user.type(within(dialog).getByRole('textbox', { name: 'token (required)' }), SECRET)

    const fade = holdAnimations()
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect([
      ...document.querySelectorAll('input, textarea'),
    ].map(f => (f as HTMLInputElement).value)).not.toContain(SECRET)
    await user.click(addButton())
    const again = screen.getByRole('dialog', { name: 'Add a provider' })
    expect((within(again).getByRole('textbox', { name: 'Name (required)' }) as HTMLInputElement).value).toBe('')
    expect(within(again).queryByRole('textbox', { name: 'token (required)' })).toBeNull()
    await fade.release()
  })

  it('reads the list again when a create fails, so an instance the server made before its answer was lost shows', async () => {
    const providers = household([])
    const { user } = renderProviders({
      'GET /providers': providers.route,
      'POST /providers': () => {
        providers.state.list = [
          instance('forecast', 'open-meteo'),
        ]
        throw new TypeError('Failed to fetch')
      },
    })
    const dialog = await openCreate(user)
    await fill(user, dialog, { name: 'forecast', module: 'open-meteo' })
    await user.click(within(dialog).getByRole('button', { name: 'Add the provider' }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server could not be reached.')
    expect(await screen.findByText('forecast', { selector: 'td *, td' })).toBeTruthy()
  })

  it('gives the focus back to its button when it is cancelled', async () => {
    const { server, user } = renderProviders()
    const dialog = await openCreate(user)
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    await expectFocusOn(addButton())
    expect(server.requests.some(r => r.method === 'POST')).toBe(false)
  })
})

describe('deleting a provider', () => {
  it('asks first, then deletes it and gives the focus to the page\'s action', async () => {
    const providers = household([
      instance('flux', 'deepgram-flux', ['apiKey']),
    ])
    const { server, user } = renderProviders({
      'GET /providers': providers.route,
      'DELETE /providers/flux': () => {
        providers.state.list = []
        return new Response(null, { status: 204 })
      },
    })
    await user.click(await screen.findByRole('button', { name: 'Delete flux' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Delete flux?' })
    expect(within(dialog).getByText(/while a setting names it/)).toBeTruthy()
    await user.click(within(dialog).getByRole('button', { name: 'Delete the provider' }))
    expect(await screen.findByText('No providers yet')).toBeTruthy()
    expect(server.calls('DELETE /providers/flux')).toBe(1)
    await waitFor(() => expect(document.activeElement).toBe(addButton()))
  })

  it('words an instance in use by its code alone, inside the dialog, which stays open', async () => {
    const { server, user } = renderProviders({
      'DELETE /providers/flux': json({ error: 'in_use' }, 409),
    })
    await user.click(await screen.findByRole('button', { name: 'Delete flux' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Delete flux?' })
    await user.click(within(dialog).getByRole('button', { name: 'Delete the provider' }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server answered 409 in_use.')
    expect(dialog.isConnected).toBe(true)
    expect(screen.getByText('flux')).toBeTruthy()
    expect(server.calls('GET /providers')).toBe(1)
  })

  it('sends nothing when cancelled, and gives the focus back', async () => {
    const { server, user } = renderProviders({
      'DELETE /providers/flux': noContent(),
    })
    const trigger = await screen.findByRole('button', { name: 'Delete flux' })
    await user.click(trigger)
    const dialog = await screen.findByRole('alertdialog', { name: 'Delete flux?' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(server.calls('DELETE /providers/flux')).toBe(0)
    await expectFocusOn(trigger)
  })
})

describe('editing a provider', () => {
  const EDIT_MODULE: ModuleInfo = {
    id: 'editable',
    kind: 'player',
    configSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['label'],
      properties: {
        label: { type: 'string', minLength: 1 },
      },
    },
    secrets: [
      { name: 'token', required: true },
      { name: 'cookies', required: false },
    ],
  }
  const CURRENT: Instance = {
    ...instance('editable', 'editable', ['token', 'cookies']),
    config: { label: 'before' },
  }

  function renderEdit(current = CURRENT) {
    return renderProviders({
      'GET /providers': json([current]),
      'GET /providers/modules': json([...CATALOG, EDIT_MODULE]),
      'PATCH /providers/editable': json(current),
    })
  }

  async function openEdit(user: UserEvent) {
    await user.click(await screen.findByRole('button', { name: 'Edit editable' }))
    return screen.findByRole('dialog', { name: 'Edit editable' })
  }

  it('prefills the config, keeps empty secrets, and leaves the existing Test action after saving', async () => {
    const { server, user } = renderEdit()
    const dialog = await openEdit(user)
    const name = within(dialog).getByRole('textbox', { name: 'Name (required)' }) as HTMLInputElement
    expect(name.value).toBe('editable')
    expect(name.readOnly).toBe(true)
    expect(moduleSelect(dialog).value).toBe('editable')
    expect(moduleSelect(dialog).disabled).toBe(true)
    const label = within(dialog).getByRole('textbox', { name: 'label (required)' }) as HTMLInputElement
    expect(label.value).toBe('before')
    const token = within(dialog).getByRole('textbox', { name: 'token' }) as HTMLInputElement
    const cookies = within(dialog).getByRole('textbox', { name: 'cookies' }) as HTMLInputElement
    expect(token.value).toBe('')
    expect(cookies.value).toBe('')
    expect(within(dialog).getAllByText('Set. Leave empty to keep it.')).toHaveLength(2)

    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await server.requests.find(r => r.method === 'PATCH')!.clone().json()).toEqual({
      config: { label: 'before' },
    })
    expect(await screen.findByRole('button', { name: 'Test editable' })).toBeTruthy()
  })

  it('replaces a typed secret and clears an optional one, with undo before saving', async () => {
    const { server, queryClient, user } = renderEdit()
    const dialog = await openEdit(user)
    await user.type(within(dialog).getByRole('textbox', { name: 'token' }), SECRET)
    await user.click(within(dialog).getByRole('button', { name: 'Clear cookies' }))
    expect((within(dialog).getByRole('textbox', { name: 'cookies' }) as HTMLInputElement).disabled).toBe(true)
    expect(within(dialog).getByText('Will be cleared when you save.')).toBeTruthy()
    await user.click(within(dialog).getByRole('button', { name: 'Undo clear cookies' }))
    expect((within(dialog).getByRole('textbox', { name: 'cookies' }) as HTMLInputElement).disabled).toBe(false)
    await user.click(within(dialog).getByRole('button', { name: 'Clear cookies' }))
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))

    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await server.requests.find(r => r.method === 'PATCH')!.clone().json()).toEqual({
      config: { label: 'before' },
      secrets: { token: SECRET },
      clearSecrets: ['cookies'],
    })
    noTraceOf(SECRET, queryClient)
  })

  it('requires a required secret only when the stored instance does not have it', async () => {
    const unset: Instance = { ...CURRENT, secrets: {} }
    const { server, user } = renderEdit(unset)
    const dialog = await openEdit(user)
    expect(within(dialog).getAllByText('Not set. Leave empty to keep it unset.')).toHaveLength(2)
    expect(within(dialog).queryByRole('button', { name: 'Clear cookies' })).toBeNull()
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))
    const token = within(dialog).getByRole('textbox', { name: 'token (required)' })
    expect(messageOf(token)).toBe('Required.')
    expect(server.requests.some(r => r.method === 'PATCH')).toBe(false)
  })

  it('uploads a cookies.txt as its text, keeps the other secret, and never shows the file back', async () => {
    const jar = '# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\tfresh-value\n'
    const { server, queryClient, user } = renderEdit()
    const dialog = await openEdit(user)
    const picker = dialog.querySelector('input[type="file"]') as HTMLInputElement
    await user.upload(picker, new File([jar], 'cookies.txt', { type: 'text/plain' }))
    expect(await within(dialog).findByText('Loaded from a file.')).toBeTruthy()
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))

    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await server.requests.find(r => r.method === 'PATCH')!.clone().json()).toEqual({
      config: { label: 'before' },
      secrets: { cookies: jar },
    })
    noTraceOf(jar, queryClient)
  })
})
