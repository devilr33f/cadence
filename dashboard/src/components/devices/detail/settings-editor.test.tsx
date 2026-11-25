// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { detail, devicePage, ID } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
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

const GLOBAL = {
  timeZone: 'Asia/Bishkek',
  language: 'ru',
  followUp: { enabled: true, windowMs: 5000, limit: 3 },
}

async function renderSettings(device: object, overrides: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: detail(),
    }, {
      'GET /settings/global': json(GLOBAL),
      [`GET /devices/${ID}/settings`]: json(device),
      ...overrides,
    }),
    pages: { '/devices/$id': DevicePage },
  })
  await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
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

describe('the device\'s settings', () => {
  it('show each key\'s value in force: the device\'s own, else the global one, else the server\'s default', async () => {
    await renderSettings({ language: 'en', historyTurns: 10 })
    expect(cells('Language')).toEqual(['Languagelanguage', 'en', 'Overridden'])
    expect(cells('Time zone')).toEqual(['Time zonetimeZone', 'Asia/Bishkek', 'Inherited'])
    expect(cells('History turns')).toEqual(['History turnshistoryTurns', '10', 'Overridden'])
    expect(cells('Follow-up')).toEqual(['Follow-upfollowUp', 'on, 5000 ms window, 3 in a row', 'Inherited'])
    expect(cells('Speech to text')).toEqual(['Speech to textstt', '—', 'Server default'])
    // keys only the global scope sets are not offered here
    expect(screen.queryByRole('row', { name: 'Home Assistant' })).toBeNull()
  })

  it('reset an override back to the inherited value with null', async () => {
    const { server, user } = await renderSettings({ language: 'en' }, {
      [`PUT /devices/${ID}/settings`]: json({}),
    })
    await user.click(within(screen.getByRole('row', { name: 'Language' })).getByRole('button', { name: 'Reset Language' }))
    await waitFor(() => expect(cells('Language')).toEqual(['Languagelanguage', 'ru', 'Inherited']))
    const put = server.requests.find(r => r.method === 'PUT')!
    expect(await put.clone().json()).toEqual({ language: null })
    // reset is gone with the override: focus lands on the row's Override
    expect(document.activeElement).toBe(within(screen.getByRole('row', { name: 'Language' })).getByRole('button', { name: 'Override Language' }))
  })

  it.each(['Follow-up', 'Quiet hours'])('name what %s needs when nothing is in force, and focus it', async (label) => {
    const { server, user } = await renderSettings({}, {
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
    })
    await user.click(within(screen.getByRole('row', { name: label })).getByRole('button', { name: `Override ${label}` }))
    await user.click(screen.getByRole('button', { name: 'Save the override' }))
    const group = screen.getByRole('group', { name: `${label} (required)` })
    expect(within(group).getByRole('alert').textContent).toBe('Required.')
    expect(group.contains(document.activeElement)).toBe(true)
    expect(server.requests.some(r => r.method === 'PUT')).toBe(false)
  })

  it('move focus into the editor, and back to the row when it closes', async () => {
    const { user } = await renderSettings({}, {
      [`PUT /devices/${ID}/settings`]: json({ historyTurns: 30 }),
    })
    const row = () => screen.getByRole('row', { name: 'History turns' })
    await user.click(within(row()).getByRole('button', { name: 'Override History turns' }))
    expect(document.activeElement).toBe(screen.getByRole('textbox', { name: 'History turns (required)' }))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(document.activeElement).toBe(within(row()).getByRole('button', { name: 'Override History turns' }))
    await user.click(within(row()).getByRole('button', { name: 'Override History turns' }))
    await user.type(screen.getByRole('textbox', { name: 'History turns (required)' }), '30')
    await user.click(screen.getByRole('button', { name: 'Save the override' }))
    await waitFor(() => expect(document.activeElement).toBe(within(row()).getByRole('button', { name: 'Edit History turns' })))
  })

  it('override an inherited key with a value of its type', async () => {
    const { server, user } = await renderSettings({}, {
      [`PUT /devices/${ID}/settings`]: json({ historyTurns: 30 }),
    })
    await user.click(within(screen.getByRole('row', { name: 'History turns' })).getByRole('button', { name: 'Override History turns' }))
    const field = screen.getByRole('textbox', { name: 'History turns (required)' })
    await user.clear(field)
    await user.type(field, '30')
    await user.click(screen.getByRole('button', { name: 'Save the override' }))
    await waitFor(() => expect(cells('History turns')).toEqual(['History turnshistoryTurns', '30', 'Overridden']))
    const put = server.requests.find(r => r.method === 'PUT')!
    expect(await put.clone().json()).toEqual({ historyTurns: 30 })
  })

  it('edit an override starting from its value, and an object key field by field', async () => {
    const { server, user } = await renderSettings({
      followUp: { enabled: true, windowMs: 8000, limit: 2 },
    }, {
      [`PUT /devices/${ID}/settings`]: json({
        followUp: { enabled: false, windowMs: 8000, limit: 2 },
      }),
    })
    await user.click(within(screen.getByRole('row', { name: 'Follow-up' })).getByRole('button', { name: 'Edit Follow-up' }))
    const group = screen.getByRole('group', { name: 'Follow-up (required)' })
    expect((within(group).getByRole('textbox', { name: 'windowMs (required)' }) as HTMLInputElement).value).toBe('8000')
    await user.click(within(group).getByRole('switch', { name: 'enabled (required)' }))
    await user.click(screen.getByRole('button', { name: 'Save the override' }))
    await waitFor(() => expect(cells('Follow-up')).toEqual(['Follow-upfollowUp', 'off, 8000 ms window, 2 in a row', 'Overridden']))
    expect(await server.requests.find(r => r.method === 'PUT')!.clone().json()).toEqual({
      followUp: { enabled: false, windowMs: 8000, limit: 2 },
    })
  })

  it('show the server\'s refusal beside the override, in codes', async () => {
    const { user } = await renderSettings({}, {
      [`PUT /devices/${ID}/settings`]: json({ error: 'settings_invalid' }, 422),
    })
    await user.click(within(screen.getByRole('row', { name: 'Time zone' })).getByRole('button', { name: 'Override Time zone' }))
    const field = screen.getByRole('textbox', { name: 'Time zone (required)' })
    await user.clear(field)
    await user.type(field, 'Mars/Base')
    await user.click(screen.getByRole('button', { name: 'Save the override' }))
    expect((await screen.findByRole('alert')).textContent).toBe('The server answered 422 settings_invalid.')
  })
})
