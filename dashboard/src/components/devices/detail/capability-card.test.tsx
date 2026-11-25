import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ALARMS, capability, detail, devicePage, ID, SOLOIST } from '@/test/device-detail'
import { json, noContent } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
// @vitest-environment jsdom
import { expectFocusOn } from '@/test/focus'
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
})

async function renderDevice(overrides: Record<string, FakeRoute> = {}, current = {
  detail: detail(),
}) {
  const app = renderApp({
    path: `/devices/${ID}`,
    routes: devicePage(current, overrides),
    pages: { '/devices/$id': DevicePage },
  })
  await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
  return {
    ...app,
    user: setupUser(),
  }
}

function card(name: string) {
  return screen.getByRole('region', { name })
}

const CAP = `/devices/${ID}/capabilities`

// the generic card, on a capability carrying the alarms' schemas: alarms itself has its own summary and tab (rule 7)
const WITH_SCHEDULE = {
  detail: detail({
    capabilities: [
      { ...ALARMS, id: 'schedule' },
      ...detail().capabilities!.filter(c => c.id !== 'alarms'),
    ],
  }),
}

describe('the desired configuration', () => {
  it('is edited through the form of the schema the device declares, and a 422 lands on the field its path names', async () => {
    let attempt = 0
    const { server, user } = await renderDevice({
      [`PUT ${CAP}/schedule/config`]: () => ++attempt === 1
        ? Response.json({ error: 'config_invalid', path: '/timeZone' }, { status: 422 })
        : Response.json({ generation: 4 }),
    }, WITH_SCHEDULE)
    const alarms = card('schedule')
    expect(within(alarms).getByText('Asia/Bishkek')).toBeTruthy()
    await user.click(within(alarms).getByRole('button', { name: 'Edit the configuration' }))
    expect(within(alarms).queryByLabelText('Configuration of schedule (JSON)')).toBeNull()
    const zone = within(alarms).getByRole('textbox', { name: 'timeZone (required)' }) as HTMLInputElement
    expect(zone.value).toBe('Asia/Bishkek')
    expect(document.activeElement).toBe(zone)

    await user.clear(zone)
    await user.type(zone, 'Mars/Base')
    await user.click(within(alarms).getByRole('button', { name: 'Save the configuration' }))
    expect((await within(alarms).findByRole('alert')).textContent).toBe('The server refused this value (config_invalid).')
    await expectFocusOn(zone)
    const first = server.requests.filter(r => r.method === 'PUT')[0]!
    expect(await first.clone().json()).toEqual({ timeZone: 'Mars/Base' })
    expect(first.headers.get('X-Cadence-CSRF')).toBe('1')

    await user.clear(zone)
    await user.type(zone, 'Europe/Riga')
    await user.click(within(alarms).getByRole('button', { name: 'Save the configuration' }))
    await waitFor(() => expect(within(alarms).queryByRole('textbox', { name: 'timeZone (required)' })).toBeNull())
    await expectFocusOn(within(alarms).getByRole('button', { name: 'Edit the configuration' }))
    expect(await server.requests.filter(r => r.method === 'PUT')[1]!.clone().json()).toEqual({ timeZone: 'Europe/Riga' })
    // the detail is read again for the new generation
    await waitFor(() => expect(server.calls(`GET /devices/${ID}`)).toBeGreaterThanOrEqual(2))
  })

  it('is raw json while the device declares no schema, and a 422 shows the server\'s path beside it', async () => {
    const { server, user } = await renderDevice({
      [`PUT ${CAP}/audio.output/config`]: json({ error: 'config_invalid', path: '/level' }, 422),
    })
    const output = card('audio.output')
    await user.click(within(output).getByRole('button', { name: 'Edit the configuration' }))
    const editor = within(output).getByLabelText('Configuration of audio.output (JSON)') as HTMLTextAreaElement
    expect(JSON.parse(editor.value)).toEqual({})
    expect(document.activeElement).toBe(editor)
    expect(within(output).queryByText(/the form cannot show/)).toBeNull()
    await user.clear(editor)
    await user.type(editor, '{{"level": 101}')
    await user.click(within(output).getByRole('button', { name: 'Save the configuration' }))
    expect((await within(output).findByRole('alert')).textContent).toBe('The server answered 422 config_invalid at /level.')
    expect(await server.requests.find(r => r.method === 'PUT')!.clone().json()).toEqual({ level: 101 })
  })

  it('gives focus back to Edit when the editor is cancelled', async () => {
    const { user } = await renderDevice({}, WITH_SCHEDULE)
    const alarms = card('schedule')
    await user.click(within(alarms).getByRole('button', { name: 'Edit the configuration' }))
    await user.click(within(alarms).getByRole('button', { name: 'Cancel' }))
    expect(document.activeElement).toBe(within(alarms).getByRole('button', { name: 'Edit the configuration' }))
  })

  it('keeps an edit in progress when the desired generation moves underneath it', async () => {
    const current = {
      detail: WITH_SCHEDULE.detail,
    }
    const { user, queryClient } = await renderDevice({}, current)
    const alarms = card('schedule')
    await user.click(within(alarms).getByRole('button', { name: 'Edit the configuration' }))
    const zone = within(alarms).getByRole('textbox', { name: 'timeZone (required)' })
    await user.clear(zone)
    await user.type(zone, 'Europe/Riga')
    current.detail = detail({
      capabilities: WITH_SCHEDULE.detail.capabilities!.map(c => c.id === 'schedule'
        ? { ...c, desiredGen: 4 }
        : c),
    })
    await queryClient.invalidateQueries()
    await waitFor(() => expect(within(card('schedule')).getByText('gen 3 → 4')).toBeTruthy())
    expect((within(card('schedule')).getByRole('textbox', { name: 'timeZone (required)' }) as HTMLInputElement).value).toBe('Europe/Riga')
  })

  it('says when nothing is set and the device\'s own defaults apply', async () => {
    await renderDevice()
    expect(within(card('audio.output')).getByText('Nothing set: the device\'s defaults apply.')).toBeTruthy()
  })
})

describe('a capability\'s availability', () => {
  it('is marked unavailable while the device says its hardware is absent, and unmarked otherwise', async () => {
    await renderDevice({}, {
      detail: detail({
        capabilities: [
          capability({
            id: 'display.screen',
            available: false,
            state: { power: 'sleep', screen: false },
          }),
          ...detail().capabilities!,
        ],
      }),
    })
    expect(within(card('display.screen')).getByText('Unavailable')).toBeTruthy()
    expect(within(card('audio.output')).queryByText('Unavailable')).toBeNull()
  })
})

describe('a capability\'s secrets', () => {
  it('are the ones the device declares, in its order and with its descriptions, each set or not', async () => {
    await renderDevice({}, {
      detail: detail({
        capabilities: [
          capability({
            id: 'media.soloist',
            secretSpecs: [
              { name: 'clientId' },
              { name: 'apiKey', description: 'The Soloist API key' },
            ],
            secrets: [
              { name: 'apiKey', description: 'The Soloist API key', set: true },
            ],
          }),
        ],
      }),
    })
    const soloist = card('media.soloist')
    const rows = within(soloist).getAllByRole('listitem')
    expect(rows).toEqual([
      within(soloist).getByRole('listitem', { name: 'clientId' }),
      within(soloist).getByRole('listitem', { name: 'apiKey' }),
    ])
    expect(within(rows[0]!).getByText('Not set')).toBeTruthy()
    expect(within(rows[1]!).getByText('Set')).toBeTruthy()
    expect(within(rows[1]!).getByText('The Soloist API key')).toBeTruthy()
  })

  it('are replaced write-only: the value is sent once, cleared, and never shown or kept', async () => {
    const VALUE = 'sk-soloist-0123456789'
    const current = {
      detail: detail(),
    }
    const { server, user, queryClient } = await renderDevice({
      [`PUT ${CAP}/media.soloist/secrets/apiKey`]: () => {
        current.detail = detail({
          capabilities: detail().capabilities!.map(c => c.id === SOLOIST.id
            ? {
                ...SOLOIST,
                secrets: [
                  { ...SOLOIST.secrets![0]!, set: true },
                ],
              }
            : c),
        })
        return new Response(null, { status: 204 })
      },
    }, current)
    const soloist = card('media.soloist')
    const secret = within(soloist).getByRole('listitem', { name: 'apiKey' })
    expect(within(secret).getByText('Not set')).toBeTruthy()
    expect(within(secret).getByText('The Soloist API key')).toBeTruthy()

    await user.click(within(secret).getByRole('button', { name: 'Set apiKey' }))
    const input = within(secret).getByLabelText('New value for apiKey') as HTMLInputElement
    expect(document.activeElement).toBe(input)
    await user.type(input, VALUE)
    // uncontrolled: react writes a controlled input's value back as its attribute
    expect(input.getAttribute('value')).toBeNull()
    await user.click(within(secret).getByRole('button', { name: 'Save the secret' }))

    await waitFor(() => expect(within(card('media.soloist')).getByText('Set')).toBeTruthy())
    const put = server.requests.find(r => r.method === 'PUT')!
    expect(await put.clone().json()).toEqual({ value: VALUE })
    expect(within(card('media.soloist')).queryByLabelText('New value for apiKey')).toBeNull()
    expect(document.body.innerHTML).not.toContain(VALUE)
    expect([
      ...document.querySelectorAll('input, textarea'),
    ].map(f => (f as HTMLInputElement).value)).not.toContain(VALUE)
    await expectFocusOn(within(card('media.soloist')).getByRole('button', { name: 'Replace apiKey' }))
    const cached = queryClient.getQueryCache().getAll().map(q => JSON.stringify(q.state.data) ?? '')
    expect(cached.join('')).not.toContain(VALUE)
    expect(queryClient.getMutationCache().getAll()).toHaveLength(0)
  })

  it('is a field no password manager fills or offers to save', async () => {
    const { user } = await renderDevice()
    const secret = within(card('media.soloist')).getByRole('listitem', { name: 'apiKey' })
    await user.click(within(secret).getByRole('button', { name: 'Set apiKey' }))
    const input = within(secret).getByLabelText('New value for apiKey') as HTMLInputElement
    expect(input.type).toBe('text')
    expect(input.getAttribute('autocomplete')).toBe('off')
    expect(input.getAttribute('name') ?? '').not.toMatch(/pass/i)
    expect(input.hasAttribute('data-1p-ignore')).toBe(true)
    expect(input.getAttribute('data-lpignore')).toBe('true')
    expect(input.hasAttribute('data-bwignore')).toBe(true)
    expect(input.getAttribute('data-form-type')).toBe('other')
    expect(input.className).toContain('[-webkit-text-security:disc]')
  })

  it('gives focus back to Set when the field is cancelled, and says so when nothing was typed', async () => {
    const { server, user } = await renderDevice()
    const secret = within(card('media.soloist')).getByRole('listitem', { name: 'apiKey' })
    await user.click(within(secret).getByRole('button', { name: 'Set apiKey' }))
    await user.click(within(secret).getByRole('button', { name: 'Save the secret' }))
    expect(within(secret).getByRole('alert').textContent).toBe('Type the new value.')
    expect(server.requests.some(r => r.method === 'PUT')).toBe(false)
    await user.click(within(secret).getByRole('button', { name: 'Cancel' }))
    expect(document.activeElement).toBe(within(secret).getByRole('button', { name: 'Set apiKey' }))
  })

  it('clears the field when the server refuses the value', async () => {
    const { user } = await renderDevice({
      [`PUT ${CAP}/media.soloist/secrets/apiKey`]: json({ error: 'secret_invalid' }, 422),
    })
    const secret = within(card('media.soloist')).getByRole('listitem', { name: 'apiKey' })
    await user.click(within(secret).getByRole('button', { name: 'Set apiKey' }))
    await user.type(within(secret).getByLabelText('New value for apiKey'), 'x')
    await user.click(within(secret).getByRole('button', { name: 'Save the secret' }))
    expect((await within(secret).findByRole('alert')).textContent).toBe('The server answered 422 secret_invalid.')
    expect((within(secret).getByLabelText('New value for apiKey') as HTMLInputElement).value).toBe('')
  })

  it('are deleted after a confirmation', async () => {
    const set = {
      ...SOLOIST,
      secrets: [
        { ...SOLOIST.secrets![0]!, set: true },
      ],
    }
    const current = {
      detail: detail({
        capabilities: [set],
      }),
    }
    const { server, user } = await renderDevice({
      [`DELETE ${CAP}/media.soloist/secrets/apiKey`]: () => {
        current.detail = detail({
          capabilities: [SOLOIST],
        })
        return new Response(null, { status: 204 })
      },
    }, current)
    const secret = within(card('media.soloist')).getByRole('listitem', { name: 'apiKey' })
    await user.click(within(secret).getByRole('button', { name: 'Delete apiKey' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Delete the secret' }))
    await waitFor(() => expect(within(card('media.soloist')).getByText('Not set')).toBeTruthy())
    expect(server.calls(`DELETE ${CAP}/media.soloist/secrets/apiKey`)).toBe(1)
    // the delete button went with the secret: focus lands on the row's Set
    await waitFor(() => expect(document.activeElement).toBe(within(card('media.soloist')).getByRole('button', { name: 'Set apiKey' })))
  })
})

describe('a capability\'s commands', () => {
  it('runs one through the form of its arguments\' schema and shows the device\'s result', async () => {
    const { server, user } = await renderDevice({
      [`POST ${CAP}/schedule/commands/set_timer`]: json({
        result: { id: 't1', ok: true },
      }),
    }, WITH_SCHEDULE)
    const alarms = card('schedule')
    expect(within(alarms).getAllByRole('button', { name: /^(?:cancel|set)_/ }).map(b => b.textContent)).toEqual([
      'cancel_alarm',
      'set_alarm',
      'set_timer',
    ])
    await user.click(within(alarms).getByRole('button', { name: 'set_timer' }))
    expect(within(alarms).queryByLabelText('Arguments of set_timer (JSON)')).toBeNull()
    await user.type(within(alarms).getByRole('textbox', { name: 'sessionId (required)' }), 's1')
    await user.type(within(alarms).getByRole('textbox', { name: 'id (required)' }), 't1')
    await user.type(within(alarms).getByRole('textbox', { name: 'seconds (required)' }), '600')
    await user.click(within(alarms).getByRole('button', { name: 'Send set_timer' }))

    const result = await within(alarms).findByRole('status', { name: 'Result of set_timer' })
    expect(JSON.parse(result.querySelector('pre')!.textContent!)).toEqual({ id: 't1', ok: true })
    const post = server.requests.find(r => r.method === 'POST')!
    expect(new URL(post.url).pathname).toBe(`/api/v1${CAP}/schedule/commands/set_timer`)
    expect(await post.clone().json()).toEqual({ sessionId: 's1', id: 't1', seconds: 600 })
  })

  it('takes the arguments as json when the form cannot show their schema, and says so', async () => {
    const { server, user } = await renderDevice({
      [`POST ${CAP}/schedule/commands/set_alarm`]: json({
        result: { id: 'a1', ok: true },
      }),
    }, WITH_SCHEDULE)
    const alarms = card('schedule')
    await user.click(within(alarms).getByRole('button', { name: 'set_alarm' }))
    expect(within(alarms).getByText('Edited as JSON: the form cannot show this schema.')).toBeTruthy()
    const args = within(alarms).getByLabelText('Arguments of set_alarm (JSON)')
    await user.clear(args)
    await user.type(args, '{{"sessionId": "s1", "id": "a1", "time": "07:30"}')
    await user.click(within(alarms).getByRole('button', { name: 'Send set_alarm' }))

    await within(alarms).findByRole('status', { name: 'Result of set_alarm' })
    expect(await server.requests.find(r => r.method === 'POST')!.clone().json()).toEqual({ sessionId: 's1', id: 'a1', time: '07:30' })
  })

  it('takes the arguments as json, without a note, when the command declares no schema', async () => {
    const { server, user } = await renderDevice({
      [`POST ${CAP}/audio.output/commands/set_level`]: json({ result: null }),
    })
    const output = card('audio.output')
    await user.click(within(output).getByRole('button', { name: 'set_level' }))
    expect(within(output).queryByText(/the form cannot show/)).toBeNull()
    const args = within(output).getByLabelText('Arguments of set_level (JSON)')
    await user.clear(args)
    await user.type(args, '{{"level": 20}')
    await user.click(within(output).getByRole('button', { name: 'Send set_level' }))
    await within(output).findByRole('status', { name: 'Result of set_level' })
    expect(await server.requests.find(r => r.method === 'POST')!.clone().json()).toEqual({ level: 20 })
  })

  it('says why the device refused, with its own code', async () => {
    const { user } = await renderDevice({
      [`POST ${CAP}/schedule/commands/cancel_alarm`]: json({ error: 'command_failed', detail: 'no_session' }, 409),
    }, WITH_SCHEDULE)
    const alarms = card('schedule')
    await user.click(within(alarms).getByRole('button', { name: 'cancel_alarm' }))
    await user.click(within(alarms).getByRole('button', { name: 'Send cancel_alarm' }))
    expect((await within(alarms).findByRole('alert')).textContent).toBe('The server answered 409 command_failed (no_session).')
  })

  it('puts a 422\'s path beside the arguments', async () => {
    const { user } = await renderDevice({
      [`POST ${CAP}/schedule/commands/set_alarm`]: json({ error: 'invalid_arguments', path: '/time' }, 422),
    }, WITH_SCHEDULE)
    const alarms = card('schedule')
    await user.click(within(alarms).getByRole('button', { name: 'set_alarm' }))
    await user.click(within(alarms).getByRole('button', { name: 'Send set_alarm' }))
    expect((await within(alarms).findByRole('alert')).textContent).toBe('The server answered 422 invalid_arguments at /time.')
  })
})

it('sends a delete only once confirmed', async () => {
  const set = {
    ...SOLOIST,
    secrets: [
      { ...SOLOIST.secrets![0]!, set: true },
    ],
  }
  const { server, user } = await renderDevice({
    [`DELETE ${CAP}/media.soloist/secrets/apiKey`]: noContent(),
  }, {
    detail: detail({
      capabilities: [set],
    }),
  })
  const secret = within(card('media.soloist')).getByRole('listitem', { name: 'apiKey' })
  const trigger = within(secret).getByRole('button', { name: 'Delete apiKey' })
  await user.click(trigger)
  const dialog = await screen.findByRole('alertdialog')
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(server.calls(`DELETE ${CAP}/media.soloist/secrets/apiKey`)).toBe(0)
  // a cancel gives the focus back to the button that opened it, which is still there
  await waitFor(() => expect(dialog.isConnected).toBe(false))
  await expectFocusOn(trigger)
})
