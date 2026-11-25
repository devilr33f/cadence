// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { capability, detail, devicePage, ID } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { DevicePage } from './device-page'

const COMMANDS = `/devices/${ID}/capabilities`

const NO_ARGS = { argsSchema: null }

const CEC = capability({
  id: 'display.cec',
  state: {
    adapter: {
      present: true,
      physicalAddress: '2.0.0.0',
      logicalAddress: 4,
      deviceType: 'playback',
    },
    systemAudio: { available: true, enabled: 'off' },
    volumeTarget: 'tv',
    devices: [
      {
        logicalAddress: 0,
        type: 'tv',
        name: 'SIM TV',
        vendorId: 240,
        power: 'on',
        audioStatus: { volume: 40, muted: false },
        supports: ['power', 'volume'],
      },
      {
        logicalAddress: 5,
        type: 'audio_system',
        name: 'SIM AVR',
        vendorId: 0x925,
        power: 'standby',
        audioStatus: { volume: 25, muted: true },
        supports: ['power', 'volume', 'system_audio'],
      },
    ],
  },
  commands: [
    { name: 'refresh', ...NO_ARGS },
    { name: 'power', ...NO_ARGS },
    { name: 'volume', ...NO_ARGS },
    { name: 'system_audio', ...NO_ARGS },
    { name: 'active_source', ...NO_ARGS },
  ],
})

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function recording(body: unknown = { result: null }, status = 200) {
  const bodies: unknown[] = []
  const route: FakeRoute = async (request) => {
    bodies.push(await request.json())
    return Response.json(body, { status })
  }
  return { bodies, route }
}

function renderCec(overrides: Record<string, FakeRoute> = {}, capabilities = [CEC]) {
  return renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: detail({ capabilities, online: true, model: 'sim-tv' }),
    }, overrides),
    pages: { '/devices/$id': DevicePage },
  })
}

async function cec() {
  return screen.findByRole('region', { name: 'display.cec' })
}

describe('the hdmi cec card', () => {
  it('lists the wire\'s devices with their state and the buttons they take', async () => {
    renderCec()
    const card = await cec()
    const tv = within(card).getByText('SIM TV').closest('li')!
    expect(tv).toBeTruthy()
    expect(within(tv).getByText(/TV · address 0 · Samsung · On · volume 40%/)).toBeTruthy()
    expect(within(tv).getAllByRole('button').map(b => b.getAttribute('aria-label') ?? b.textContent))
      .toEqual([
        'Volume down on SIM TV',
        'Mute SIM TV',
        'Volume up on SIM TV',
        'Turn off',
      ])
    // the volume target is the tv while system audio is off: the avr shows no volume keys
    const avr = within(card).getByText('SIM AVR').closest('li')!
    expect(within(avr).getByText(/Receiver · address 5 · Pulse-Eight · Standby · volume 25%/)).toBeTruthy()
    expect(within(avr).getAllByRole('button').map(b => b.textContent)).toEqual(['Turn on'])
    expect(within(card).getByText(
      (_, el) => el?.tagName === 'SPAN' && el.textContent === 'Audio through the receiver: off',
    )).toBeTruthy()
  })

  it('sends each press as one display.cec command', async () => {
    const power = recording()
    const volume = recording()
    const sam = recording()
    renderCec({
      [`POST ${COMMANDS}/display.cec/commands/power`]: power.route,
      [`POST ${COMMANDS}/display.cec/commands/volume`]: volume.route,
      [`POST ${COMMANDS}/display.cec/commands/system_audio`]: sam.route,
    })
    const card = await cec()
    const user = setupUser()
    await user.click(within(card).getByRole('button', { name: 'Turn off' }))
    await waitFor(() => expect(power.bodies).toEqual([
      { action: 'off' },
    ]))
    await user.click(within(card).getByRole('button', { name: 'Volume up on SIM TV' }))
    await waitFor(() => expect(volume.bodies).toEqual([
      { action: 'up' },
    ]))
    await user.click(within(card).getByRole('button', { name: 'Use the receiver' }))
    await waitFor(() => expect(sam.bodies).toEqual([
      { enabled: true },
    ]))
  })

  it('says a refused command in codes beside the buttons', async () => {
    renderCec({
      [`POST ${COMMANDS}/display.cec/commands/power`]: json({ error: 'command_failed', code: 'no_target' }, 409),
    })
    const card = await cec()
    const user = setupUser()
    await user.click(within(card).getByRole('button', { name: 'Turn off' }))
    expect((await within(card).findByRole('alert')).textContent).toContain('409 command_failed')
  })

  it('says a wire that is not there and offers nothing but what the device declared', async () => {
    renderCec({}, [
      capability({
        id: 'display.cec',
        state: {
          adapter: { present: false },
          systemAudio: { available: false, enabled: 'unknown' },
          devices: [],
        },
        commands: [
          { name: 'refresh', ...NO_ARGS },
        ],
      }),
    ])
    const card = await cec()
    expect(within(card).getByText('No HDMI CEC adapter on this device.')).toBeTruthy()
    expect(within(card).getAllByRole('button').map(b => b.textContent)).toEqual(['Rescan'])
  })

  it('has no accessibility violations axe can see', async () => {
    renderCec()
    const card = await cec()
    // jsdom has no canvas; axe only probes for one
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
    const results = await axe.run(card, {
      rules: {
        'color-contrast': { enabled: false },
      },
    })
    expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
  })
})
