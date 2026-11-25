// @vitest-environment jsdom
import type { Detail } from '@/api/gen'
import { cleanup, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { detail, devicePage, ID } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { DEVICES } from '@/test/household'
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
  vi.restoreAllMocks()
})

const ROOM = `PUT /devices/${ID}/room`

describe('a device\'s room', () => {
  it('is set in the header, trimmed, and shown there', async () => {
    const user = setupUser()
    const current: { detail: Detail } = {
      detail: detail(),
    }
    const bodies: unknown[] = []
    renderApp({
      path: `/devices/${ID}`,
      routes: devicePage(current, {
        'GET /devices': json(DEVICES),
        [ROOM]: async (request) => {
          const body = await request.clone().json() as { room: string }
          bodies.push(body)
          current.detail = detail({ room: body.room })
          return Response.json(current.detail)
        },
      }),
      pages: { '/devices/$id': DevicePage },
    })
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const set = screen.getByRole('button', { name: 'Set the room of Kitchen' })
    await user.click(set)
    const field = screen.getByRole('combobox', { name: 'Room' }) as HTMLInputElement
    await expectFocusOn(field)
    await user.type(field, '  Гостиная ')
    await user.click(screen.getByRole('button', { name: 'Save the room' }))
    await waitFor(() => expect(screen.queryByRole('combobox', { name: 'Room' })).toBeNull())
    expect(bodies).toEqual([
      { room: 'Гостиная' },
    ])
    expect(screen.getByText('Гостиная')).toBeTruthy()
    await expectFocusOn(screen.getByRole('button', { name: 'Set the room of Kitchen' }))
  })

  it('words a room the server refused on the field', async () => {
    const user = setupUser()
    renderApp({
      path: `/devices/${ID}`,
      routes: devicePage({
        detail: detail(),
      }, {
        [ROOM]: json({ error: 'invalid', path: '/body/room' }, 422),
      }),
      pages: { '/devices/$id': DevicePage },
    })
    await user.click(await screen.findByRole('button', { name: 'Set the room of Kitchen' }))
    const field = screen.getByRole('combobox', { name: 'Room' })
    await user.type(field, 'Hall')
    await user.click(screen.getByRole('button', { name: 'Save the room' }))
    expect((await screen.findByRole('alert')).textContent).toContain('invalid')
    expect(field.getAttribute('aria-invalid')).toBe('true')
  })
})
