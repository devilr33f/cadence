// @vitest-environment jsdom
import type { UserEvent } from '@testing-library/user-event'
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ALARMS, detail, devicePage, ID } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { METRICS, metricsBody, metricsRoute, PROCESSES, series } from '@/test/health'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { DevicePage } from '../device-page'

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

const METRICS_PATH = `GET /devices/${ID}/metrics`
const PROCESSES_PATH = `GET /devices/${ID}/processes`

function renderHealth(overrides: Record<string, FakeRoute> = {}, capabilities = [ALARMS, METRICS]) {
  const app = renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: detail({ capabilities }),
    }, {
      [METRICS_PATH]: metricsRoute,
      [PROCESSES_PATH]: json(PROCESSES),
      ...overrides,
    }),
    pages: { '/devices/$id': DevicePage },
  })
  const reads = () => app.server.requests
    .filter(r => new URL(r.url).pathname.endsWith('/metrics'))
    .map((r) => {
      const params = new URL(r.url).searchParams
      return {
        names: params.getAll('name'),
        since: Date.parse(params.get('since') ?? ''),
      }
    })
  return { ...app, reads }
}

async function openHealth(user: UserEvent) {
  await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
  await user.click(screen.getByRole('tab', { name: 'Health' }))
  return screen.getByRole('tabpanel', { name: 'Health' })
}

function namesOf(request: Request): string[] {
  return new URL(request.url).searchParams.getAll('name')
}

function card(panel: HTMLElement, name: string) {
  return within(panel).getByRole('region', { name })
}

function rowsOf(table: HTMLElement) {
  return within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').map(cell => cell.textContent))
}

describe('a device\'s health', () => {
  it('reads nothing until its tab is opened, then each chart\'s names over the last hour, and the top processes', async () => {
    const user = setupUser()
    const { server, reads } = renderHealth()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    await screen.findByText('Simulator')
    expect(reads()).toEqual([])
    expect(server.calls(PROCESSES_PATH)).toBe(0)

    const panel = await openHealth(user)
    await within(card(panel, 'CPU')).findByText('25%')
    const hourAgo = Date.now() - 3600_000
    expect(reads().map(r => r.names).sort()).toEqual([
      ['system.cpu.utilization'],
      ['system.filesystem.free', 'system.filesystem.limit'],
      ['system.memory.used', 'system.memory.limit'],
      ['system.temperature'],
    ])
    for (const { since } of reads())
      expect(Math.abs(since - hourAgo)).toBeLessThan(10_000)
    expect(server.calls(PROCESSES_PATH)).toBe(1)
  })

  it('draws each metric\'s latest value as its figure, and says the rest in words', async () => {
    const user = setupUser()
    renderHealth()
    const panel = await openHealth(user)

    const cpu = card(panel, 'CPU')
    await within(cpu).findByText('25%')
    // the total, not a core's
    expect(within(cpu).getByRole('group', { name: 'CPU over the last hour: latest 25%, highest 50%, lowest 25%' })).toBeTruthy()

    const memory = card(panel, 'Memory')
    await within(memory).findByText('411 MiB')
    expect(within(memory).getByText('used of 1.94 GiB')).toBeTruthy()
    expect(within(memory).getByRole('group', { name: 'Memory used over the last hour: latest 411 MiB, highest 411 MiB, lowest 381 MiB' })).toBeTruthy()

    const temperature = card(panel, 'Temperature')
    expect(await within(temperature).findByText('47.3 °C')).toBeTruthy()

    const storage = card(panel, 'Storage')
    const data = await within(storage).findByRole('meter', { name: '/data used' })
    expect(data.getAttribute('aria-valuetext')).toBe('70% used')
    expect(within(storage).getByText('1.21 GiB free of 4 GiB')).toBeTruthy()
    expect(within(storage).getByRole('meter', { name: '/tmp used' }).getAttribute('aria-valuetext')).toBe('22% used')
    expect(within(storage).getByText('100 MiB free of 128 MiB')).toBeTruthy()
  })

  it('reads a point out as the keyboard moves along the line, and lists the points as a table', async () => {
    const user = setupUser()
    renderHealth()
    const panel = await openHealth(user)
    const cpu = card(panel, 'CPU')
    const chart = await within(cpu).findByRole('group', { name: /^CPU over the last hour/ })

    chart.focus()
    await waitFor(() => expect(within(cpu).getByRole('status').textContent).toMatch(/^25% at \d\d:\d\d:\d\d$/))
    await user.keyboard('{ArrowLeft}')
    expect(within(cpu).getByRole('status').textContent).toMatch(/^50% at \d\d:\d\d:\d\d$/)
    await user.keyboard('{End}')
    expect(within(cpu).getByRole('status').textContent).toMatch(/^25% at/)

    await user.click(within(cpu).getByText('Points'))
    const table = within(cpu).getByRole('table', { name: 'CPU points' })
    expect(rowsOf(table).map(([, value]) => value)).toEqual(['25%', '50%'])
  })

  it('reads again over another window, the old chart saying its own window until the new one lands', async () => {
    const user = setupUser()
    let release = () => {}
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    const { reads } = renderHealth({
      [METRICS_PATH]: async (request) => {
        const since = Date.parse(new URL(request.url).searchParams.get('since') ?? '')
        if (Date.now() - since > 2 * 3600_000)
          await held
        return metricsRoute(request)
      },
    })
    const panel = await openHealth(user)
    await within(card(panel, 'CPU')).findByText('25%')
    await user.selectOptions(within(panel).getByRole('combobox', { name: 'Window' }), 'Last 24 hours')
    await waitFor(() => expect(reads()).toHaveLength(8))
    const dayAgo = Date.now() - 86_400_000
    for (const { since } of reads().slice(4))
      expect(Math.abs(since - dayAgo)).toBeLessThan(10_000)
    // the faded chart still draws the hour it read
    expect(within(card(panel, 'CPU')).getByRole('group', { name: /^CPU over the last hour/ })).toBeTruthy()
    release()
    expect(await within(card(panel, 'CPU')).findByRole('group', { name: /^CPU over the last 24 hours/ })).toBeTruthy()
  })

  it('charts each mount\'s used share over the window under its meter', async () => {
    const user = setupUser()
    const gib = 1024 ** 3
    renderHealth({
      [METRICS_PATH]: request => Response.json(namesOf(request).includes('system.filesystem.free')
        ? {
            truncated: false,
            series: [
              series('system.filesystem.free', { mountpoint: '/data' }, [3 * gib, 2 * gib, gib]),
              series('system.filesystem.limit', { mountpoint: '/data' }, [4 * gib, 4 * gib, 4 * gib]),
            ],
          }
        : metricsBody(namesOf(request))),
    })
    const panel = await openHealth(user)
    const storage = card(panel, 'Storage')
    expect((await within(storage).findByRole('meter', { name: '/data used' })).getAttribute('aria-valuetext')).toBe('75% used')
    expect(within(storage).getByText('1 GiB free of 4 GiB')).toBeTruthy()
    expect(within(storage).getByRole('group', { name: '/data used over the last hour: latest 75%, highest 75%, lowest 25%' })).toBeTruthy()
  })

  it('lists a mount whose size it never reported, with no meter or chart', async () => {
    const user = setupUser()
    renderHealth({
      [METRICS_PATH]: request => Response.json(namesOf(request).includes('system.filesystem.free')
        ? {
            truncated: false,
            series: [
              series('system.filesystem.free', { mountpoint: '/data' }, [1_300_000_000]),
              series('system.filesystem.limit', { mountpoint: '/data' }, [0]),
            ],
          }
        : metricsBody(namesOf(request))),
    })
    const panel = await openHealth(user)
    const storage = card(panel, 'Storage')
    expect(await within(storage).findByText('1.21 GiB free; its size was not reported')).toBeTruthy()
    expect(within(storage).queryByRole('meter')).toBeNull()
    expect(within(storage).queryByRole('group')).toBeNull()
  })

  it('draws memory against what it used when the limit reads zero', async () => {
    const user = setupUser()
    renderHealth({
      [METRICS_PATH]: request => Response.json(namesOf(request).includes('system.memory.used')
        ? {
            truncated: false,
            series: [
              series('system.memory.used', {}, [400_000_000, 431_000_000]),
              series('system.memory.limit', {}, [0, 0]),
            ],
          }
        : metricsBody(namesOf(request))),
    })
    const panel = await openHealth(user)
    const memory = card(panel, 'Memory')
    await within(memory).findByText('411 MiB')
    expect(within(memory).getByText('used')).toBeTruthy()
    expect(memory.innerHTML).not.toContain('NaN')
  })

  it('says a table of a long window lists averages', async () => {
    const user = setupUser()
    renderHealth({
      [METRICS_PATH]: request => Response.json(namesOf(request).includes('system.cpu.utilization')
        ? {
            truncated: false,
            series: [
              series('system.cpu.utilization', {}, Array.from({ length: 300 }, (_, i) => (i % 10) / 10), 5),
            ],
          }
        : metricsBody(namesOf(request))),
    })
    const panel = await openHealth(user)
    const cpu = card(panel, 'CPU')
    await user.click(await within(cpu).findByText('Points, averaged'))
    const table = within(cpu).getByRole('table', { name: 'CPU points, averaged' })
    expect(within(table).getByRole('columnheader', { name: 'Average' })).toBeTruthy()
    expect(rowsOf(table).length).toBeLessThan(300)
  })

  it('lists the top processes by cpu and by memory, their names as text', async () => {
    const user = setupUser()
    renderHealth()
    const panel = await openHealth(user)
    const byCpu = await within(panel).findByRole('table', { name: 'Top by CPU' })
    expect(rowsOf(byCpu)).toEqual([
      ['cadenced', '812', '45%', '12.4 MiB'],
      [
        '<b>soloist</b>',
        '901',
        '12%',
        '58.2 MiB',
      ],
    ])
    expect(rowsOf(within(panel).getByRole('table', { name: 'Top by memory' }))[0]).toEqual([
      '<b>soloist</b>',
      '901',
      '12%',
      '58.2 MiB',
    ])
    expect(panel.querySelector('b')).toBeNull()
  })

  it('says so when the speaker sends no metrics, and asks for none', async () => {
    const user = setupUser()
    const { server, reads } = renderHealth({}, [ALARMS])
    const panel = await openHealth(user)
    expect(within(panel).getByText('This speaker sends no metrics')).toBeTruthy()
    expect(reads()).toEqual([])
    expect(server.calls(PROCESSES_PATH)).toBe(0)
  })

  it('says so when a window holds no points, or the server cut it', async () => {
    const user = setupUser()
    renderHealth({
      [METRICS_PATH]: request => Response.json({
        series: [],
        truncated: new URL(request.url).searchParams.getAll('name').includes('system.cpu.utilization'),
      }),
      [PROCESSES_PATH]: json({ topCpu: [], topRss: [] }),
    })
    const panel = await openHealth(user)
    expect(await within(card(panel, 'Memory')).findByText('No points in this window.')).toBeTruthy()
    expect(within(card(panel, 'Temperature')).getByText('No temperature in this window: the board may have no sensor.')).toBeTruthy()
    expect(within(card(panel, 'Storage')).getByText('No points in this window.')).toBeTruthy()
    expect(within(card(panel, 'CPU')).getByText('The server sent the first 50,000 points of this window.')).toBeTruthy()
    expect(await within(panel).findAllByText('No processes reported yet.')).toHaveLength(2)
  })

  it('words a read that failed, in codes', async () => {
    const user = setupUser()
    renderHealth({
      [METRICS_PATH]: json({ error: 'not_found' }, 404),
    })
    const panel = await openHealth(user)
    expect(await within(card(panel, 'CPU')).findByText('The server answered 404 not_found.')).toBeTruthy()
  })
})
