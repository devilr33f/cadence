// @vitest-environment jsdom
import type { SpanView, TraceStats } from '@/api/gen'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { DEVICES, KITCHEN } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { TracePage } from './trace-page'
import { TracesPage } from './traces-page'

const STATS: TraceStats = {
  enabled: true,
  sampleRatio: 1,
  liveSpans: 2,
  traces: 12,
  finished: 10,
  ringSize: 500,
  bytes: 2048,
  budget: 1024 * 1024,
  seq: 40,
  bytesIn: 1024,
  bytesOut: 0,
  dropped: 0,
  overflow: 0,
  foreign: 0,
  ingested: 3,
  rejected: 0,
  export: {
    on: true,
    sent: 1234,
    dropped: 0,
    failures: 2,
    queued: 0,
  },
}

function span(patch: Partial<SpanView> & { spanId: string }): SpanView {
  return {
    traceId: 'trace-a',
    name: patch.spanId,
    service: 'cadence-server',
    start: '2026-11-12T15:05:00.000Z',
    durationMs: 100,
    live: false,
    seq: 1,
    ...patch,
  }
}

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  localStorage.clear()
  sessionStorage.clear()
})

function sendSpan(type: string, seq: number, s: SpanView) {
  act(() => FakeSocket.last.receive({
    type,
    at: s.start,
    data: { seq, span: s },
  }))
}

describe('the traces page', () => {
  it('lists the newest traces with the store\'s figures, and folds a new span in live', async () => {
    const app = renderApp({
      path: '/traces',
      routes: {
        'GET /session': signedIn,
        'GET /settings/global': json({ timeZone: 'UTC' }),
        'GET /devices': json(DEVICES),
        'GET /traces/stats': json(STATS),
        'GET /traces': json({
          seq: 30,
          traces: [
            {
              traceId: 'trace-a',
              name: 'voice.turn',
              start: '2026-11-12T15:05:00.000Z',
              durationMs: 840,
              live: false,
              error: true,
              spans: 4,
              services: ['cadence-device', 'cadence-server'],
              deviceId: KITCHEN,
              seq: 30,
            },
          ],
        }),
      },
      pages: { '/traces/': TracesPage },
    })
    const table = await screen.findByRole('table', { name: 'Traces' })
    const link = await within(table).findByRole('link', { name: 'voice.turn' })
    expect(link.getAttribute('href')).toBe('/traces/trace-a')
    const row = link.closest('tr')!
    expect(within(row).getByText('Kitchen')).toBeTruthy()
    expect(within(row).getByText('840 ms')).toBeTruthy()
    expect(within(row).getByText('Error')).toBeTruthy()
    expect(within(row).getByText('Device')).toBeTruthy()
    expect(await screen.findByText('12 of 500')).toBeTruthy()
    expect(screen.getByText('1,234 sent · 2 failed')).toBeTruthy()

    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames()).toContainEqual({
      subscribe: ['presence', 'traces'],
    }))
    sendSpan('span_start', 31, span({
      traceId: 'trace-b',
      spanId: 'b1',
      name: 'GET /api/v1/devices',
      live: true,
      durationMs: 0,
      seq: 31,
      start: new Date().toISOString(),
    }))
    const fresh = await within(table).findByRole('link', { name: 'GET /api/v1/devices' })
    expect(within(fresh.closest('tr')!).getByText('Running')).toBeTruthy()
    // the change is read again from the store shortly after
    await waitFor(() => expect(app.server.requests.some(r => new URL(r.url).searchParams.get('since') === '30')).toBe(true), { timeout: 2500 })
  })

  it('shows a stream\'s bytes and rate while it runs', async () => {
    renderApp({
      path: '/traces',
      routes: {
        'GET /session': signedIn,
        'GET /settings/global': json({ timeZone: 'UTC' }),
        'GET /devices': json(DEVICES),
        'GET /traces/stats': json(STATS),
        'GET /traces': json({ seq: 0, traces: [] }),
      },
      pages: { '/traces/': TracesPage },
    })
    await screen.findByText('No traces yet')
    act(() => FakeSocket.last.open())
    sendSpan('span_update', 5, span({
      traceId: 't',
      spanId: 's',
      name: 'stream.relay',
      live: true,
      seq: 5,
      start: new Date().toISOString(),
      stream: {
        bytesIn: 3 * 1024 * 1024,
        bytesOut: 1024,
        speedIn: 2 * 1024 * 1024,
        speedOut: 0,
      },
    }))
    const streams = await screen.findByRole('table', { name: 'Streams' })
    expect(within(streams).getByRole('link', { name: 'stream.relay' })).toBeTruthy()
    expect(within(streams).getAllByText('2 MiB/s').length).toBeGreaterThan(0)
    expect(within(streams).getByText('3 MiB')).toBeTruthy()
  })
})

describe('a trace\'s page', () => {
  it('lays its spans out and shows the selected one with its events', async () => {
    renderApp({
      path: '/traces/trace-a',
      routes: {
        'GET /session': signedIn,
        'GET /settings/global': json({ timeZone: 'UTC' }),
        'GET /devices': json(DEVICES),
        'GET /traces/trace-a': json({
          traceId: 'trace-a',
          name: 'voice.turn',
          start: '2026-11-12T15:05:00.000Z',
          durationMs: 400,
          live: false,
          error: false,
          services: ['cadence-device', 'cadence-server'],
          seq: 9,
          rootIds: ['root'],
          spans: [
            span({
              spanId: 'root',
              name: 'voice.turn',
              service: 'cadence-device',
              deviceId: KITCHEN,
              durationMs: 400,
              events: [
                { name: 'wake_detected', at: '2026-11-12T15:05:00.050Z' },
              ],
              attrs: { 'cadence.step': 2 },
            }),
            span({
              spanId: 'stt',
              parentId: 'root',
              name: 'stt.stream',
              start: '2026-11-12T15:05:00.100Z',
              durationMs: 120,
              links: [
                { traceId: 'trace-z', spanId: 'z1' },
              ],
            }),
          ],
        }),
      },
      pages: { '/traces/$traceId': TracePage },
    })
    await screen.findByRole('heading', { level: 1, name: 'voice.turn' })
    const panel = screen.getByRole('complementary', { name: 'The selected span' })
    expect(within(panel).getByText('wake_detected')).toBeTruthy()
    expect(within(panel).getByText('+50 ms')).toBeTruthy()
    expect(within(panel).getByText('cadence.step')).toBeTruthy()
    const user = setupUser()
    await user.click(screen.getByRole('button', { name: /^stt\.stream, 120 ms from \+100 ms, ok$/ }))
    const next = screen.getByRole('complementary', { name: 'The selected span' })
    expect(within(next).getByRole('heading', { name: 'stt.stream' })).toBeTruthy()
    expect(within(next).getByRole('link', { name: 'trace-z' }).getAttribute('href')).toBe('/traces/trace-z')
  })

  it('says why a failed trace failed, and opens the failing span with its socket\'s close', async () => {
    renderApp({
      path: '/traces/trace-a',
      routes: {
        'GET /session': signedIn,
        'GET /settings/global': json({ timeZone: 'UTC' }),
        'GET /devices': json(DEVICES),
        'GET /traces/trace-a': json({
          traceId: 'trace-a',
          name: 'voice.session',
          start: '2026-11-12T15:05:00.000Z',
          durationMs: 400,
          live: false,
          error: true,
          services: ['cadence-server'],
          seq: 9,
          rootIds: ['root'],
          spans: [
            span({
              spanId: 'root',
              name: 'voice.session',
              status: 'error',
              statusCode: 'stt_unavailable',
              durationMs: 400,
            }),
            span({
              spanId: 'stt',
              parentId: 'root',
              name: 'stt.stream',
              status: 'error',
              statusCode: 'stt_unavailable',
              start: '2026-11-12T15:05:00.100Z',
            }),
            span({
              spanId: 'sock',
              parentId: 'stt',
              name: 'uniproxy.stt',
              status: 'error',
              statusCode: 'closed_1011',
              start: '2026-11-12T15:05:00.110Z',
              attrs: { 'cadence.outcome': 'closed', 'websocket.close_code': 1011 },
              events: [
                {
                  name: 'socket_end',
                  at: '2026-11-12T15:05:00.200Z',
                  attrs: { 'cadence.outcome': 'closed' },
                },
              ],
            }),
          ],
        }),
      },
      pages: { '/traces/$traceId': TracePage },
    })
    const summary = await screen.findByRole('region', { name: 'Why the trace failed' })
    expect(within(summary).getByText('voice.session › stt.stream ›')).toBeTruthy()
    expect(within(summary).getByText('closed_1011')).toBeTruthy()
    expect(within(summary).getByText('1011')).toBeTruthy()
    await setupUser().click(within(summary).getByRole('button', { name: 'uniproxy.stt' }))
    const panel = screen.getByRole('complementary', { name: 'The selected span' })
    const why = within(panel).getByRole('region', { name: 'Why it failed' })
    expect(within(why).getByText('Close code')).toBeTruthy()
    expect(within(why).getByText('closed')).toBeTruthy()
    expect(within(panel).getByText('cadence.outcome=closed')).toBeTruthy()
  })

  it('says so when the server no longer keeps the trace', async () => {
    renderApp({
      path: '/traces/gone',
      routes: {
        'GET /session': signedIn,
        'GET /settings/global': json({ timeZone: 'UTC' }),
        'GET /devices': json(DEVICES),
        'GET /traces/gone': json({ error: 'not_found' }, 404),
      },
      pages: { '/traces/$traceId': TracePage },
    })
    expect(await screen.findByText('This trace is no longer kept')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'See the recent traces' })).toBeTruthy()
  })
})
