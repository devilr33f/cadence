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

const CAP = `/devices/${ID}/capabilities/network.wifi`
const PASSWORD = 'correct-horse-9'

function wifi(state: Record<string, unknown>) {
  return capability({
    id: 'network.wifi',
    state,
    commands: [
      { name: 'scan', argsSchema: null },
    ],
    secrets: [
      { name: 'psk', description: 'the wi-fi password', set: false },
    ],
  })
}

const ON_HOME = wifi({
  state: 'completed',
  managed: false,
  ssid: 'Home',
  bssid: '02:00:00:00:00:01',
  freqMHz: 5180,
  ip: '192.0.2.10',
})

const SCAN = {
  result: {
    networks: [
      {
        ssid: 'Upstairs',
        bssid: '02:00:00:00:00:02',
        freqMHz: 2412,
        signalDbm: -48,
        security: '[WPA2-PSK-CCMP][ESS]',
      },
      {
        ssid: 'Office',
        bssid: '02:00:00:00:00:03',
        freqMHz: 5200,
        signalDbm: -72,
        security: '[WPA2-EAP-CCMP][ESS]',
      },
      {
        ssid: '',
        bssid: '02:00:00:00:00:04',
        freqMHz: 2437,
        signalDbm: -61,
        security: '[WPA2-PSK-CCMP][ESS]',
      },
    ],
  },
}

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function recording(body: unknown = { generation: 3 }, status = 200) {
  const bodies: unknown[] = []
  const route: FakeRoute = async (request) => {
    bodies.push(await request.json())
    return Response.json(body, { status })
  }
  return { bodies, route }
}

function renderWiFi(overrides: Record<string, FakeRoute> = {}, cap = ON_HOME) {
  return renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: detail({
        capabilities: [cap],
        online: true,
      }),
    }, overrides),
    pages: { '/devices/$id': DevicePage },
  })
}

async function card() {
  return screen.findByRole('region', { name: 'network.wifi' })
}

describe('the wi-fi card', () => {
  it('shows the link now and whether cadence manages it', async () => {
    renderWiFi()
    const c = await card()
    const now = within(c).getByRole('group', { name: 'Wi-Fi now' })
    expect(within(now).getByText('Home')).toBeTruthy()
    expect(within(now).getByText('5 GHz · 5180 MHz')).toBeTruthy()
    expect(within(now).getByText('192.0.2.10')).toBeTruthy()
    expect(within(now).getByText('Not managed')).toBeTruthy()
  })

  it.each([
    [
      { code: 'rolled_back', reason: 'no_server', target: 'Captive' },
      'Rolled back from Captive',
      'could not reach the server',
    ],
    [
      { code: 'rolled_back', reason: 'no_ip', target: 'Upstairs' },
      'Rolled back from Upstairs',
      'never got an address',
    ],
    [
      { code: 'switching', target: 'Upstairs' },
      'Switching to Upstairs',
      'previous network',
    ],
    [
      { code: 'psk_missing', target: 'Upstairs' },
      'No password set',
      '',
    ],
  ])('words the switch\'s outcome %#', async (extra, label, detail) => {
    renderWiFi({}, wifi({
      state: 'completed',
      managed: true,
      ssid: 'Home',
      ip: '192.0.2.10',
      ...extra,
    }))
    const c = await card()
    const badge = within(c).getByText(label).closest('[data-tone]')!
    expect(badge.textContent).toContain(detail)
  })

  it('scans, lists what it can join, and fills the name in from a pick', async () => {
    const scan = recording(SCAN)
    renderWiFi({ [`POST ${CAP}/commands/scan`]: scan.route })
    const c = await card()
    const user = setupUser()
    await user.click(within(c).getByRole('button', { name: 'Scan' }))
    const list = await within(c).findByRole('list', { name: 'Networks found' })
    expect(scan.bodies).toEqual([{}])
    expect(within(list).getByText(/Enterprise \(not supported\)/)).toBeTruthy()
    expect(within(list).getByText('Hidden network')).toBeTruthy()
    // only a wpa-psk network with a name is offered
    expect(within(list).getAllByRole('button').map(b => b.textContent)).toEqual(['Choose Upstairs'])
    await user.click(within(list).getByRole('button', { name: 'Choose Upstairs' }))
    expect((within(c).getByLabelText('Network name') as HTMLInputElement).value).toBe('Upstairs')
    expect(document.activeElement).toBe(within(c).getByLabelText('Password'))
  })

  it('sends the name and password as one configure, and keeps the password nowhere', async () => {
    const configure = recording()
    const { server, queryClient } = renderWiFi({ [`POST ${CAP}/configure`]: configure.route })
    const c = await card()
    const user = setupUser()
    await user.type(within(c).getByLabelText('Network name'), 'Upstairs')
    const password = within(c).getByLabelText('Password') as HTMLInputElement
    await user.type(password, PASSWORD)
    expect(password.type).toBe('text')
    await user.click(within(c).getByRole('button', { name: 'Connect' }))
    await waitFor(() => expect(configure.bodies).toEqual([
      {
        config: { enabled: true, ssid: 'Upstairs' },
        secrets: { psk: PASSWORD },
      },
    ]))
    expect(await within(c).findByText(/Sent\. The speaker switches to Upstairs/)).toBeTruthy()
    expect(password.value).toBe('')
    expect(document.body.innerHTML).not.toContain(PASSWORD)
    expect(server.requests.some(r => r.url.includes(PASSWORD))).toBe(false)
    expect(JSON.stringify(queryClient.getQueryCache().getAll().map(q => q.state.data))).not.toContain(PASSWORD)
    expect(JSON.stringify(queryClient.getMutationCache().getAll().map(m => m.state.variables))).not.toContain(PASSWORD)
  })

  it('refuses a short password on its field and sends nothing', async () => {
    const configure = recording()
    renderWiFi({ [`POST ${CAP}/configure`]: configure.route })
    const c = await card()
    const user = setupUser()
    await user.type(within(c).getByLabelText('Network name'), 'Upstairs')
    await user.type(within(c).getByLabelText('Password'), 'short')
    await user.click(within(c).getByRole('button', { name: 'Connect' }))
    expect((await within(c).findByRole('alert')).textContent).toBe('The password needs 8 characters at least.')
    expect(document.activeElement).toBe(within(c).getByLabelText('Password'))
    expect(configure.bodies).toEqual([])
  })

  it('says a refusal in codes beside the button', async () => {
    renderWiFi({
      [`POST ${CAP}/configure`]: json({ error: 'config_invalid', path: '/ssid' }, 422),
    })
    const c = await card()
    const user = setupUser()
    await user.type(within(c).getByLabelText('Network name'), 'Upstairs')
    await user.type(within(c).getByLabelText('Password'), PASSWORD)
    await user.click(within(c).getByRole('button', { name: 'Connect' }))
    const alert = await within(c).findByRole('alert')
    expect(alert.textContent).toContain('422 config_invalid')
    expect(alert.textContent).not.toContain(PASSWORD)
  })

  it('stops managing with enabled false', async () => {
    const put = recording({ generation: 4 })
    renderWiFi({ [`PUT ${CAP}/config`]: put.route }, wifi({
      state: 'completed',
      managed: true,
      ssid: 'Home',
      target: 'Home',
    }))
    const c = await card()
    const user = setupUser()
    await user.click(within(c).getByRole('button', { name: 'Stop managing the Wi-Fi' }))
    await waitFor(() => expect(put.bodies).toEqual([
      { enabled: false },
    ]))
  })

  it('has no accessibility violations axe can see', async () => {
    renderWiFi({ [`POST ${CAP}/commands/scan`]: recording(SCAN).route })
    const c = await card()
    await setupUser().click(within(c).getByRole('button', { name: 'Scan' }))
    await within(c).findByRole('list', { name: 'Networks found' })
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
    const results = await axe.run(c, {
      rules: {
        'color-contrast': { enabled: false },
      },
    })
    expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
  })
})
