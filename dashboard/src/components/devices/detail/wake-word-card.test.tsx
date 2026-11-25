// @vitest-environment jsdom
import type { WakeWordView } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { capability, detail, devicePage, ID } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { DevicePage } from './device-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const WORD: WakeWordView = {
  id: '0b5c3b1e-7d0c-4c5e-9a43-3d2f0f6a8b11',
  word: 'Джарвис',
  source: 'trained',
  status: 'ready',
  sha256: 'b'.repeat(64),
  modelBytes: 1,
  datasetKept: false,
  createdAt: '2026-11-20T10:00:00Z',
}

const CUSTOM = capability({
  id: 'wake.custom',
  state: { state: 'off', detections: 0 },
})

function renderCard(overrides: Record<string, FakeRoute> = {}) {
  renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: detail({
        capabilities: [CUSTOM],
      }),
    }, {
      'GET /wakewords': json([WORD]),
      ...overrides,
    }),
    pages: { '/devices/$id': DevicePage },
  })
  return setupUser()
}

describe('the custom wake word card', () => {
  it('switches the word on with its model id, sha256 and sensitivity', async () => {
    const bodies: unknown[] = []
    const user = renderCard({
      [`PUT /devices/${ID}/capabilities/wake.custom/config`]: async (req) => {
        bodies.push(await req.json())
        return Response.json({ gen: 1 })
      },
    })
    const card = await screen.findByRole('region', { name: 'wake.custom' })
    expect(within(card).getByText('Off')).toBeTruthy()
    await within(card).findByRole('option', { name: 'Джарвис' })
    await user.click(within(card).getByRole('switch', { name: 'Wake on a word of your own' }))
    await user.click(within(card).getByRole('button', { name: 'Save the wake word' }))
    await waitFor(() => expect(bodies).toEqual([
      {
        enabled: true,
        modelId: WORD.id,
        sha256: WORD.sha256,
        sensitivity: 0.5,
      },
    ]))
  })

  it('points to the wake words page while no word is ready', async () => {
    renderCard({
      'GET /wakewords': json([]),
    })
    const card = await screen.findByRole('region', { name: 'wake.custom' })
    expect((await within(card).findByRole('link', { name: 'train or upload one' })).getAttribute('href')).toBe('/wakewords')
  })
})
