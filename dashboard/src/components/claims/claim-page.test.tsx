// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, noContent, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { DEVICES } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { ClaimPage } from './claim-page'
import { ClaimsPage } from './claims-page'

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

const CLAIM = {
  code: 'K7M4QX',
  board: 'android-tv',
  source: '192.168.0.50',
  createdAt: '2026-11-14T12:00:00Z',
  expiresAt: '2099-11-14T12:10:00Z',
}

function renderClaim(overrides: Record<string, FakeRoute> = {}, path = '/claims/k7m4qx') {
  const app = renderApp({
    path,
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /boards': json([
        { id: 'android-tv', name: 'Android TV' },
      ]),
      'GET /devices': json(DEVICES),
      'GET /claims': json([CLAIM]),
      'GET /claims/K7M4QX': json(CLAIM),
      ...overrides,
    },
    pages: { '/claims/$code': ClaimPage, '/claims': ClaimsPage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

describe('approving a screen\'s claim', () => {
  it('shows the code as the screen does, and approves it with a name and a room', async () => {
    const bodies: unknown[] = []
    const { user, server } = renderClaim({
      'POST /claims/K7M4QX/approve': async (request) => {
        bodies.push(await request.clone().json())
        return Response.json({
          id: 'dev-1',
          name: 'Living TV',
          model: 'android-tv',
          room: 'Kitchen',
          status: 'pending',
        }, { status: 201 })
      },
    })
    expect(await screen.findByText('K7M · 4QX')).toBeTruthy()
    expect(screen.getByText('Android TV')).toBeTruthy()
    // the rooms the household's devices are in, suggested
    const room = screen.getByRole('combobox', { name: 'Room' }) as HTMLInputElement
    await waitFor(() => expect(document.getElementById(room.getAttribute('list')!)?.querySelectorAll('option').length).toBe(1))
    await user.type(screen.getByRole('textbox', { name: 'Name' }), ' Living TV ')
    await user.type(room, 'Kitchen')
    await user.click(screen.getByRole('button', { name: 'Approve the screen' }))
    const done = await screen.findByRole('status', { name: 'Approved' })
    expect(done.textContent).toContain('Approved — the screen connects by itself')
    expect(within(done).getByRole('link', { name: 'Open Living TV' }).getAttribute('href')).toBe('/devices/dev-1')
    expect(bodies).toEqual([
      { name: 'Living TV', room: 'Kitchen' },
    ])
    expect(server.calls('POST /claims/K7M4QX/decline')).toBe(0)
  })

  it('says a code no screen holds any more, and links to the list', async () => {
    renderClaim({
      'GET /claims/K7M4QX': json({ error: 'not_found' }, 404),
    })
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('No screen waits with this code')
    expect(within(alert).getByRole('link', { name: 'See the screens waiting' }).getAttribute('href')).toBe('/claims')
  })

  it('keeps the form on a name another device has', async () => {
    const { user } = renderClaim({
      'POST /claims/K7M4QX/approve': json({ error: 'name_taken' }, 409),
    })
    const name = await screen.findByRole('textbox', { name: 'Name' })
    await user.type(name, 'Kitchen')
    await user.click(screen.getByRole('button', { name: 'Approve the screen' }))
    expect((await screen.findByRole('alert')).textContent).toContain('name_taken')
    expect(name.getAttribute('aria-invalid')).toBe('true')
    await waitFor(() => expect(document.activeElement).toBe(name))
  })

  it('says the claim went while it was answered', async () => {
    const { user } = renderClaim({
      'POST /claims/K7M4QX/approve': json({ error: 'not_found' }, 404),
    })
    await user.type(await screen.findByRole('textbox', { name: 'Name' }), 'TV')
    await user.click(screen.getByRole('button', { name: 'Approve the screen' }))
    expect((await screen.findByRole('alert')).textContent).toContain('no longer open')
  })

  it('refuses an empty name without asking the server', async () => {
    const { user, server } = renderClaim()
    await screen.findByRole('textbox', { name: 'Name' })
    await user.click(screen.getByRole('button', { name: 'Approve the screen' }))
    expect((await screen.findByRole('alert')).textContent).toBe('Give the speaker a name.')
    expect(server.calls('POST /claims/K7M4QX/approve')).toBe(0)
  })

  it('declines it', async () => {
    const { user, server } = renderClaim({
      'POST /claims/K7M4QX/decline': noContent(),
    })
    await screen.findByRole('textbox', { name: 'Name' })
    await user.click(screen.getByRole('button', { name: 'Decline' }))
    expect((await screen.findByRole('status', { name: 'Declined' })).textContent).toContain('screen says so')
    expect(server.calls('POST /claims/K7M4QX/decline')).toBe(1)
  })

  it('lists the screens waiting, each with its approve link', async () => {
    renderClaim({}, '/claims')
    const table = await screen.findByRole('table', { name: 'Screens waiting' })
    await within(table).findByText('K7M · 4QX')
    expect(within(table).getByRole('link', { name: 'Approve K7M · 4QX' }).getAttribute('href')).toBe('/claims/K7M4QX')
  })
})
