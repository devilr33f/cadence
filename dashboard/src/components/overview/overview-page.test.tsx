// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { OverviewPage } from './overview-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const KITCHEN = '0193a71e-55c0-7e2b-b1a4-6f0d93c2e7aa'
const BEDROOM = '0193a71e-55c0-7e2b-b1a4-6f0d93c2e7bb'

function device(id: string, name: string, online: boolean) {
  return {
    id,
    name,
    model: 'sim',
    status: 'enrolled',
    online,
    lastSeenAt: null,
  }
}

const household: Record<string, FakeRoute> = {
  'GET /session': signedIn,
  'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
  'GET /devices': json([
    device(KITCHEN, 'Kitchen', true),
    device(BEDROOM, 'Bedroom', false),
    {
      id: 'p1',
      name: 'Hall',
      model: 'sim',
      status: 'pending',
      online: false,
      lastSeenAt: null,
    },
  ]),
}

function live() {
  act(() => {
    FakeSocket.last.open()
    FakeSocket.last.receive({
      type: 'subscribed',
      topics: [
        'devices',
        'sessions',
        'agent',
        'tasks',
      ],
    })
  })
}

function send(frame: object) {
  act(() => FakeSocket.last.receive(frame))
}

describe('the overview', () => {
  it('counts the speakers online, offline and enrolling from the list, fetching no detail', async () => {
    const { server } = renderApp({ path: '/', routes: household, home: OverviewPage })
    const speakers = await screen.findByRole('list', { name: 'Speakers by state' })
    expect(await within(speakers).findByText('1 online')).toBeTruthy()
    expect(within(speakers).getByText('1 offline')).toBeTruthy()
    expect(within(speakers).getByText('1 enrolling')).toBeTruthy()
    expect(server.requests.some(r => new URL(r.url).pathname.startsWith('/api/v1/devices/'))).toBe(false)
  })

  it('subscribes to the activity it shows and lists it as a timeline, newest first, in the household\'s zone', async () => {
    renderApp({ path: '/', routes: household, home: OverviewPage })
    await screen.findByText('1 online')
    live()
    expect(FakeSocket.last.frames()[0]).toEqual({
      subscribe: [
        'devices',
        'sessions',
        'agent',
        'tasks',
        'presence',
      ],
    })
    expect(screen.getByText('Nothing has happened yet')).toBeTruthy()

    const turn = { deviceId: KITCHEN, sessionId: 's1', turnId: 't1' }
    send({
      type: 'turn_start',
      ...turn,
      at: '2026-11-12T15:04:10Z',
      data: { trigger: 'wake' },
    })
    send({
      type: 'transcript',
      ...turn,
      at: '2026-11-12T15:04:11Z',
      data: { text: 'what is the weather' },
    })
    send({
      type: 'llm_request',
      ...turn,
      at: '2026-11-12T15:04:11Z',
      data: { step: 1, model: 'm' },
    })
    send({
      type: 'tool_call',
      ...turn,
      at: '2026-11-12T15:04:11Z',
      data: {
        step: 1,
        id: 'c1',
        name: 'weather',
        args: '{"day":"today"}',
      },
    })
    send({
      type: 'tool_result',
      ...turn,
      at: '2026-11-12T15:04:12Z',
      data: {
        step: 1,
        id: 'c1',
        name: 'weather',
        ok: true,
        code: '',
        result: { text: 'sunny' },
        ms: 230,
      },
    })
    send({
      type: 'llm_text',
      ...turn,
      at: '2026-11-12T15:04:12Z',
      data: {
        step: 2,
        text: 'It is sunny.',
        interim: false,
        spoken: true,
      },
    })
    send({
      type: 'turn_end',
      ...turn,
      at: '2026-11-12T15:04:13Z',
      data: {
        code: 'ok',
        silent: false,
        steps: 2,
        toolCalls: 1,
        ms: 1800,
      },
    })
    send({
      type: 'task_event',
      deviceId: BEDROOM,
      at: '2026-11-12T15:04:14Z',
      data: {
        short_id: 'ab12',
        task_kind: 'hermes',
        kind: 'failed',
        status: 'failed',
        seq: 3,
      },
    })

    const timeline = screen.getByRole('list', { name: 'Recent activity' })
    const stops = within(timeline).getAllByRole('listitem').map(li => li.textContent)
    expect(stops).toHaveLength(7)
    expect(stops[0]).toContain('21:04:14')
    expect(stops[0]).toContain('Bedroom')
    expect(stops[0]).toContain('Task ab12 failed')
    expect(stops[1]).toContain('Turn ended: 2 steps, 1 tool call, 1.8 s')
    expect(stops[2]).toContain('Said: «It is sunny.»')
    expect(stops[3]).toContain('weather answered in 230 ms')
    expect(stops[4]).toContain('Called weather')
    expect(stops[5]).toContain('Heard: «what is the weather»')
    expect(stops[6]).toContain('Kitchen')
    expect(stops[6]).toContain('Woken by voice')
    expect(stops[6]).toContain('21:04:10')
  })

  it('leaves media sessions to the device pages, which follow them', async () => {
    renderApp({ path: '/', routes: household, home: OverviewPage })
    await screen.findByText('1 online')
    live()
    send({
      type: 'media_state',
      deviceId: KITCHEN,
      at: '2026-11-12T15:04:10Z',
      data: {
        state: 'playing',
        metadata: { title: 'A', artist: 'B' },
      },
    })
    expect(screen.getByText('Nothing has happened yet')).toBeTruthy()
  })

  it('says why the speakers could not be counted, in codes, with a way to try again', async () => {
    renderApp({
      path: '/',
      routes: {
        ...household,
        'GET /devices': json({ error: 'forbidden' }, 403),
      },
      home: OverviewPage,
    })
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('The server answered 403 forbidden.')
    expect(within(alert).getByRole('button', { name: 'Try again' })).toBeTruthy()
  })

  it('teaches what will show when there are no speakers yet', async () => {
    renderApp({
      path: '/',
      routes: {
        ...household,
        'GET /devices': json([]),
      },
      home: OverviewPage,
    })
    expect(await screen.findByText('No speakers yet')).toBeTruthy()
  })
})
