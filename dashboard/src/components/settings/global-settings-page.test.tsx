import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
// @vitest-environment jsdom
import { expectFocusOn } from '@/test/focus'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { GlobalSettingsPage } from './global-settings-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const GLOBAL = {
  timeZone: 'Asia/Bishkek',
  language: 'ru',
  homeAssistant: 'home',
  followUp: { enabled: true, windowMs: 5000, limit: 3 },
}

// the global put replaces the whole set (the server's PutGlobal), so it answers what it was sent, nulls dropped
function stored(): FakeRoute {
  return async (request) => {
    const body = await request.clone().json() as Record<string, unknown>
    return Response.json(Object.fromEntries(Object.entries(body).filter(([, v]) => v !== null)))
  }
}

async function renderSettings(routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: '/settings',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json(GLOBAL),
      ...routes,
    },
    pages: { '/settings': GlobalSettingsPage },
  })
  await screen.findByRole('heading', { level: 1, name: 'Settings' })
  await screen.findByRole('row', { name: 'Language' })
  return {
    ...app,
    user: setupUser(),
  }
}

function cells(name: string): string[] {
  const row = screen.getByRole('row', { name })
  return within(row).getAllByRole('cell').slice(0, 3).map(cell => cell.textContent ?? '')
}

function row(name: string) {
  return screen.getByRole('row', { name })
}

describe('the global settings', () => {
  it('list every key the global scope sets, with its value or the server\'s default', async () => {
    await renderSettings()
    expect(cells('Language')).toEqual(['Languagelanguage', 'ru', 'Set'])
    expect(cells('Follow-up')).toEqual(['Follow-upfollowUp', 'on, 5000 ms window, 3 in a row', 'Set'])
    expect(cells('Home Assistant')).toEqual(['Home AssistanthomeAssistant', 'home', 'Set'])
    expect(cells('Speech to text')).toEqual(['Speech to textstt', '—', 'Server default'])
    for (const label of [
      'Hermes',
      'Sensitive home devices',
      'Home Assistant favorites',
      'MCP servers',
      'Telemetry export',
      'Tracing',
    ])
      expect(row(label)).toBeTruthy()
    // the integrations page links a key's row
    expect(row('Hermes').id).toBe('setting-hermes')
    // a device's own key is not offered here
    expect(screen.queryByRole('row', { name: 'Home Assistant area' })).toBeNull()
    expect(within(row('Language')).getByRole('button', { name: 'Edit Language' })).toBeTruthy()
    expect(within(row('Speech to text')).getByRole('button', { name: 'Set Speech to text' })).toBeTruthy()
  })

  it('gather the telemetry keys in their own section, saying what the server does while they are unset', async () => {
    await renderSettings()
    const telemetry = screen.getByRole('table', { name: 'Telemetry' })
    expect(within(telemetry).getAllByRole('row').slice(1).map(r => r.getAttribute('aria-label'))).toEqual([
      'Log retention (days)',
      'Telemetry export',
      'Tracing',
    ])
    expect(cells('Log retention (days)')).toEqual(['Log retention (days)logRetentionDays', '7', 'Server default'])
    expect(cells('Telemetry export')).toEqual(['Telemetry exporttelemetryExportManage the providers', 'off', 'Server default'])
    // the instance an export names is made on the providers page, from the otlp-http module
    expect(within(row('Telemetry export')).getByRole('link', { name: 'Manage the providers' }).getAttribute('href')).toBe('/providers')
  })

  it('show a telemetry key the household set as it is', async () => {
    renderApp({
      path: '/settings',
      routes: {
        'GET /session': signedIn,
        'GET /settings/global': json({ ...GLOBAL, logRetentionDays: 14, telemetryExport: 'collector' }),
      },
      pages: { '/settings': GlobalSettingsPage },
    })
    await screen.findByRole('row', { name: 'Telemetry export' })
    await waitFor(() => expect(cells('Telemetry export')[1]).toBe('collector'))
    expect(cells('Log retention (days)')).toEqual(['Log retention (days)logRetentionDays', '14', 'Set'])
  })

  it('link the keys that name modes to the modes page, where those modes are made', async () => {
    await renderSettings()
    for (const name of ['Mode', 'Allowed modes', 'Compaction mode', 'Memory mode'])
      expect(within(row(name)).getByRole('link', { name: 'Manage the modes' }).getAttribute('href')).toBe('/modes')
    expect(within(row('Mode revert')).queryByRole('link')).toBeNull()
  })

  it('set a key, sending the whole set the server replaces, and give the focus back to the row', async () => {
    const { server, user } = await renderSettings({
      'PUT /settings/global': stored(),
    })
    await user.click(within(row('History turns')).getByRole('button', { name: 'Set History turns' }))
    const field = screen.getByRole('textbox', { name: 'History turns (required)' })
    expect(document.activeElement).toBe(field)
    await user.type(field, '30')
    await user.click(screen.getByRole('button', { name: 'Save the setting' }))
    await waitFor(() => expect(cells('History turns')).toEqual(['History turnshistoryTurns', '30', 'Set']))
    const put = server.requests.find(r => r.method === 'PUT')!
    expect(put.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(await put.clone().json()).toEqual({ ...GLOBAL, historyTurns: 30 })
    await waitFor(() => expect(document.activeElement).toBe(within(row('History turns')).getByRole('button', { name: 'Edit History turns' })))
    // the other keys are still set
    expect(cells('Language')).toEqual(['Languagelanguage', 'ru', 'Set'])
  })

  it('read the set again before a save, keeping a key another writer set since the page loaded', async () => {
    let reads = 0
    const { server, user } = await renderSettings({
      'GET /settings/global': () => {
        reads += 1
        return Response.json(reads === 1
          ? GLOBAL
          : { ...GLOBAL, weatherLocation: 'Bishkek' })
      },
      'PUT /settings/global': stored(),
    })
    await user.click(within(row('History turns')).getByRole('button', { name: 'Set History turns' }))
    await user.type(screen.getByRole('textbox', { name: 'History turns (required)' }), '30')
    await user.click(screen.getByRole('button', { name: 'Save the setting' }))
    await waitFor(() => expect(cells('History turns')).toEqual(['History turnshistoryTurns', '30', 'Set']))
    expect(await server.requests.find(r => r.method === 'PUT')!.clone().json()).toEqual({
      ...GLOBAL,
      weatherLocation: 'Bishkek',
      historyTurns: 30,
    })
  })

  it('reset a key to the server\'s default with null, keeping the others', async () => {
    const { server, user } = await renderSettings({
      'PUT /settings/global': stored(),
    })
    await user.click(within(row('Language')).getByRole('button', { name: 'Reset Language' }))
    await waitFor(() => expect(cells('Language')).toEqual(['Languagelanguage', '—', 'Server default']))
    const put = server.requests.find(r => r.method === 'PUT')!
    expect(await put.clone().json()).toEqual({ ...GLOBAL, language: null })
    expect(document.activeElement).toBe(within(row('Language')).getByRole('button', { name: 'Set Language' }))
    expect(cells('Time zone')).toEqual(['Time zonetimeZone', 'Asia/Bishkek', 'Set'])
  })

  it('edit an object key starting from its value', async () => {
    const { server, user } = await renderSettings({
      'PUT /settings/global': stored(),
    })
    await user.click(within(row('Follow-up')).getByRole('button', { name: 'Edit Follow-up' }))
    const group = screen.getByRole('group', { name: 'Follow-up (required)' })
    const windowMs = within(group).getByRole('textbox', { name: 'windowMs (required)' })
    await user.clear(windowMs)
    await user.type(windowMs, '8000')
    await user.click(screen.getByRole('button', { name: 'Save the setting' }))
    await waitFor(() => expect(cells('Follow-up')).toEqual(['Follow-upfollowUp', 'on, 8000 ms window, 3 in a row', 'Set']))
    expect((await server.requests.find(r => r.method === 'PUT')!.clone().json()).followUp).toEqual({ enabled: true, windowMs: 8000, limit: 3 })
  })

  it('put the server\'s 422 on the field its path names', async () => {
    const { user } = await renderSettings({
      'PUT /settings/global': json({ error: 'settings_invalid', path: '/logRetentionDays' }, 422),
    })
    await user.click(within(row('Log retention (days)')).getByRole('button', { name: 'Set Log retention (days)' }))
    const field = screen.getByRole('textbox', { name: 'Log retention (days) (required)' })
    await user.type(field, '14')
    await user.click(screen.getByRole('button', { name: 'Save the setting' }))
    await waitFor(() => expect(field.getAttribute('aria-invalid')).toBe('true'))
    expect(document.getElementById(`${field.id}-error`)?.textContent).toBe('The server refused this value (settings_invalid).')
    await expectFocusOn(field)
  })

  it('show a refusal without a path beside the form, in codes', async () => {
    const { user } = await renderSettings({
      'PUT /settings/global': json({ error: 'settings_invalid' }, 422),
    })
    await user.click(within(row('Time zone')).getByRole('button', { name: 'Edit Time zone' }))
    const field = screen.getByRole('textbox', { name: 'Time zone (required)' })
    await user.clear(field)
    await user.type(field, 'Mars/Base')
    await user.click(screen.getByRole('button', { name: 'Save the setting' }))
    expect((await screen.findByRole('alert')).textContent).toBe('The server answered 422 settings_invalid.')
    expect(cells('Time zone')).toEqual(['Time zonetimeZone', 'Asia/Bishkek', 'Set'])
  })

  it('keep the focus on the save while it runs and after a refusal beside the form', async () => {
    let answer!: (response: Response) => void
    const { server, user } = await renderSettings({
      'PUT /settings/global': () => new Promise<Response>((resolve) => {
        answer = resolve
      }),
    })
    await user.click(within(row('History turns')).getByRole('button', { name: 'Set History turns' }))
    await user.type(screen.getByRole('textbox', { name: 'History turns (required)' }), '30')
    const save = screen.getByRole('button', { name: 'Save the setting' })
    await user.click(save)
    await waitFor(() => expect(server.calls('PUT /settings/global')).toBe(1))
    expect(save.getAttribute('aria-disabled')).toBe('true')
    expect((save as HTMLButtonElement).disabled).toBe(false)
    await user.click(save)
    answer(Response.json({ error: 'unavailable' }, { status: 503 }))
    expect((await screen.findByRole('alert')).textContent).toBe('The server answered 503 unavailable.')
    expect(document.activeElement).toBe(save)
    expect(save.getAttribute('aria-disabled')).toBeNull()
    expect(server.calls('PUT /settings/global')).toBe(1)
  })

  it('keep the focus on Reset while it runs and after a refusal', async () => {
    let answer!: (response: Response) => void
    const { server, user } = await renderSettings({
      'PUT /settings/global': () => new Promise<Response>((resolve) => {
        answer = resolve
      }),
    })
    const reset = within(row('Language')).getByRole('button', { name: 'Reset Language' })
    await user.click(reset)
    await waitFor(() => expect(server.calls('PUT /settings/global')).toBe(1))
    expect(reset.getAttribute('aria-disabled')).toBe('true')
    expect((reset as HTMLButtonElement).disabled).toBe(false)
    await user.click(reset)
    answer(Response.json({ error: 'unavailable' }, { status: 503 }))
    expect((await screen.findByRole('alert')).textContent).toBe('The server answered 503 unavailable.')
    expect(document.activeElement).toBe(reset)
    expect(server.calls('PUT /settings/global')).toBe(1)
  })

  it('check a list item against its pattern before sending it', async () => {
    const { server, user } = await renderSettings()
    await user.click(within(row('Home Assistant favorites')).getByRole('button', { name: 'Set Home Assistant favorites' }))
    const item = screen.getByRole('textbox', { name: /Home Assistant favorites/ })
    await user.type(item, 'Kitchen Light{Enter}')
    await user.click(screen.getByRole('button', { name: 'Save the setting' }))
    expect(screen.getAllByRole('alert').map(a => a.textContent).join(' ')).toContain('Item 1: Must match')
    expect(server.requests.some(r => r.method === 'PUT')).toBe(false)
  })
})

it('focuses the linked setting\'s own button, so the next tab starts at the row the integrations page named', async () => {
  renderApp({
    path: '/settings#setting-homeAssistant',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json(GLOBAL),
    },
    pages: { '/settings': GlobalSettingsPage },
  })
  const row = await screen.findByRole('row', { name: 'Home Assistant' })
  await waitFor(() => expect(document.activeElement).toBe(within(row).getByRole('button', { name: 'Edit Home Assistant' })))
})
