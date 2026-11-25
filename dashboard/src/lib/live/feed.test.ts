import type { FeedStatus, LiveFeedOptions } from './feed'
import type { LiveEvent } from './frames'
import type { Topic } from './topics'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FakeSocket } from '@/test/fake-socket'
import { LiveFeed } from './feed'

beforeEach(() => {
  vi.useFakeTimers()
  FakeSocket.reset()
})

afterEach(() => {
  vi.useRealTimers()
})

function makeFeed(overrides: Partial<LiveFeedOptions> = {}) {
  const events: LiveEvent[] = []
  const statuses: FeedStatus[] = []
  const resyncs: (readonly Topic[])[] = []
  const refused: string[] = []
  const checkSession = overrides.checkSession ?? vi.fn(async () => true)
  const onSignedOut = vi.fn()
  const feed = new LiveFeed({
    url: () => 'ws://dashboard.test/api/v1/live',
    socket: FakeSocket,
    random: () => 0.5,
    onEvent: e => events.push(e),
    onStatus: s => statuses.push(s),
    onResync: t => resyncs.push(t),
    onRefused: (error, detail) => refused.push(`${error}:${detail ?? ''}`),
    checkSession,
    onSignedOut,
    ...overrides,
  })
  return {
    feed,
    events,
    statuses,
    resyncs,
    refused,
    checkSession,
    onSignedOut,
  }
}

function connect(socket = FakeSocket.last) {
  socket.open()
  socket.receive({
    type: 'subscribed',
    topics: [
      'devices',
      'sessions',
      'agent',
      'tasks',
      'media',
    ],
  })
  return socket
}

describe('subscriptions', () => {
  it('subscribes to the union of what the pages ask, each change replacing the selection', async () => {
    const { feed } = makeFeed()
    const overview = feed.want(['sessions', 'agent'])
    feed.start()
    const socket = connect()
    expect(socket.url).toBe('ws://dashboard.test/api/v1/live')
    expect(socket.frames()).toEqual([
      {
        subscribe: ['sessions', 'agent'],
      },
    ])

    const logs = feed.want(['agent', 'logs'])
    await vi.advanceTimersByTimeAsync(250)
    expect(socket.frames().at(-1)).toEqual({
      subscribe: ['sessions', 'agent', 'logs'],
    })

    // logs only while a page asks for them
    logs()
    await vi.advanceTimersByTimeAsync(250)
    expect(socket.frames().at(-1)).toEqual({
      subscribe: ['sessions', 'agent'],
    })

    overview()
    await vi.advanceTimersByTimeAsync(250)
    expect(socket.frames().at(-1)).toEqual({ subscribe: [] })
  })

  it('sends a page\'s topics once the socket opens, and nothing the selection already holds', async () => {
    const { feed } = makeFeed()
    feed.start()
    feed.want(['tasks'])
    feed.want(['tasks'])
    const socket = connect()
    await vi.advanceTimersByTimeAsync(1000)
    expect(socket.frames()).toEqual([
      {
        subscribe: ['tasks'],
      },
    ])
  })

  it('sends nothing when a page swap releases and wants the same topics in one tick, and a real change once', async () => {
    const { feed } = makeFeed()
    let page = feed.want(['sessions', 'agent'])
    feed.start()
    const socket = connect()
    await vi.advanceTimersByTimeAsync(1000)

    page()
    page = feed.want(['agent', 'sessions'])
    await vi.advanceTimersByTimeAsync(1000)
    expect(socket.frames()).toEqual([
      {
        subscribe: ['sessions', 'agent'],
      },
    ])

    page()
    page = feed.want(['tasks'])
    await vi.advanceTimersByTimeAsync(1000)
    expect(socket.frames()).toEqual([
      {
        subscribe: ['sessions', 'agent'],
      },
      {
        subscribe: ['tasks'],
      },
    ])
  })

  it('stays within the server\'s client limits however fast the pages change', async () => {
    const { feed } = makeFeed()
    feed.start()
    const socket = connect()
    const sentAt: number[] = []
    const send = socket.send.bind(socket)
    socket.send = (data: string) => {
      sentAt.push(Date.now())
      expect(new TextEncoder().encode(data).length).toBeLessThanOrEqual(4096)
      send(data)
    }

    const topics: Topic[] = [
      'devices',
      'sessions',
      'agent',
      'tasks',
      'media',
      'logs',
    ]
    for (let i = 0; i < 200; i++) {
      const release = feed.want([topics[i % topics.length]!])
      await vi.advanceTimersByTimeAsync(5)
      release()
    }
    feed.want(['media'])
    await vi.advanceTimersByTimeAsync(1000)

    for (const at of sentAt)
      expect(sentAt.filter(t => t > at - 1000 && t <= at).length).toBeLessThanOrEqual(5)
    expect(socket.frames().at(-1)).toEqual({
      subscribe: ['media'],
    })
  })
})

describe('reconnecting', () => {
  it('backs off from 1 s to 30 s', async () => {
    const { feed, statuses } = makeFeed()
    feed.start()
    connect()
    const waits: number[] = []
    for (let i = 0; i < 7; i++) {
      FakeSocket.last.open()
      const before = FakeSocket.instances.length
      FakeSocket.last.serverClose(1006)
      const closedAt = Date.now()
      while (FakeSocket.instances.length === before)
        await vi.advanceTimersByTimeAsync(10)
      waits.push(Date.now() - closedAt)
    }
    expect(waits).toEqual([
      1000,
      2000,
      4000,
      8000,
      16000,
      30000,
      30000,
    ])
    expect(statuses).toContain('reconnecting')
  })

  it('spreads each wait with jitter, never under 1 s nor over 30 s', async () => {
    const waitsWith = async (random: number) => {
      FakeSocket.reset()
      const { feed } = makeFeed({
        random: () => random,
      })
      feed.start()
      const waits: number[] = []
      for (let i = 0; i < 7; i++) {
        FakeSocket.last.open()
        const before = FakeSocket.instances.length
        FakeSocket.last.serverClose(1006)
        const closedAt = Date.now()
        while (FakeSocket.instances.length === before)
          await vi.advanceTimersByTimeAsync(10)
        waits.push(Date.now() - closedAt)
      }
      feed.stop()
      return waits
    }
    expect(await waitsWith(0)).toEqual([
      1000,
      1500,
      3000,
      6000,
      12000,
      22500,
      22500,
    ])
    expect(await waitsWith(0.9999999)).toEqual([
      1250,
      2500,
      5000,
      10000,
      20000,
      30000,
      30000,
    ])
  })

  it('starts over at 1 s only after a connection that held for 10 s', async () => {
    const { feed } = makeFeed()
    feed.start()
    const waitAfterClose = async (code: number) => {
      const before = FakeSocket.instances.length
      FakeSocket.last.serverClose(code)
      const closedAt = Date.now()
      while (FakeSocket.instances.length === before)
        await vi.advanceTimersByTimeAsync(10)
      return Date.now() - closedAt
    }

    connect()
    expect(await waitAfterClose(1006)).toBe(1000)
    // the server answered, then dropped at once: still backing off
    connect()
    expect(await waitAfterClose(1006)).toBe(2000)
    connect()
    await vi.advanceTimersByTimeAsync(9_990)
    expect(await waitAfterClose(1001)).toBe(4000)
    connect()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(await waitAfterClose(1001)).toBe(1000)
  })

  it('reports connecting, then live, then reconnecting', async () => {
    const { feed, statuses } = makeFeed()
    feed.start()
    connect()
    FakeSocket.last.serverClose(1006)
    expect(statuses).toEqual(['connecting', 'live', 'reconnecting'])
    await vi.advanceTimersByTimeAsync(1000)
    expect(statuses.at(-1)).toBe('connecting')
  })

  it('refetches what the gap may have missed after a reconnect or a drop, not on the first connect', async () => {
    const { feed, resyncs } = makeFeed()
    feed.want(['tasks', 'media'])
    feed.start()
    connect()
    expect(resyncs).toEqual([])

    FakeSocket.last.receive({ type: 'dropped', count: 3 })
    expect(resyncs).toEqual([
      ['tasks', 'media'],
    ])

    FakeSocket.last.serverClose(1006)
    await vi.advanceTimersByTimeAsync(1000)
    connect()
    expect(resyncs).toEqual([
      ['tasks', 'media'],
      ['tasks', 'media'],
    ])
  })

  it('says why each resync came: a drop, a topic that joined or a reconnect', async () => {
    const causes: string[] = []
    const { feed } = makeFeed({
      onResync: (_topics, cause) => causes.push(cause),
    })
    feed.want(['tasks'])
    feed.start()
    const socket = connect()
    socket.receive({ type: 'dropped', count: 3 })
    feed.want(['logs'])
    await vi.advanceTimersByTimeAsync(250)
    socket.serverClose(1006)
    await vi.advanceTimersByTimeAsync(1000)
    connect()
    expect(causes).toEqual(['dropped', 'joined', 'reconnect'])
  })

  it('refetches what a topic joining a live subscription feeds, its events before the join never delivered', async () => {
    const { feed, resyncs } = makeFeed()
    const tasks = feed.want(['tasks'])
    feed.start()
    const socket = connect()
    expect(resyncs).toEqual([])

    const memory = feed.want(['agent', 'tasks'])
    await vi.advanceTimersByTimeAsync(250)
    expect(socket.frames().at(-1)).toEqual({
      subscribe: ['agent', 'tasks'],
    })
    expect(resyncs).toEqual([
      ['agent'],
    ])

    // a topic that leaves misses nothing the page still shows
    tasks()
    memory()
    feed.want(['agent'])
    await vi.advanceTimersByTimeAsync(250)
    expect(socket.frames().at(-1)).toEqual({
      subscribe: ['agent'],
    })
    expect(resyncs).toEqual([
      ['agent'],
    ])
  })
})

describe('the session', () => {
  it('checks the session on a 1008 close and stops for good when it ended', async () => {
    const { feed, checkSession, onSignedOut, statuses } = makeFeed({
      checkSession: vi.fn(async () => false),
    })
    feed.start()
    connect()
    FakeSocket.last.serverClose(1008, 'unauthorized')
    await vi.advanceTimersByTimeAsync(0)

    expect(checkSession).toHaveBeenCalledOnce()
    expect(onSignedOut).toHaveBeenCalledOnce()
    expect(statuses.at(-1)).toBe('closed')
    await vi.advanceTimersByTimeAsync(120_000)
    expect(FakeSocket.instances).toHaveLength(1)
  })

  it('reconnects with backoff after a 1008 when the session still holds', async () => {
    const { feed, checkSession, onSignedOut } = makeFeed()
    feed.start()
    connect()
    FakeSocket.last.serverClose(1008, 'rate_limited')
    await vi.advanceTimersByTimeAsync(999)
    expect(checkSession).toHaveBeenCalledOnce()
    expect(FakeSocket.instances).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(FakeSocket.instances).toHaveLength(2)
    expect(onSignedOut).not.toHaveBeenCalled()
  })

  it('checks the session when the upgrade never opened (a refused upgrade reaches the page as 1006)', async () => {
    const { feed, checkSession, onSignedOut } = makeFeed({
      checkSession: vi.fn(async () => false),
    })
    feed.start()
    FakeSocket.last.serverClose(1006)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(checkSession).toHaveBeenCalledOnce()
    expect(onSignedOut).toHaveBeenCalledOnce()
    expect(FakeSocket.instances).toHaveLength(1)
  })

  it('keeps retrying when the session cannot be checked', async () => {
    const { feed, onSignedOut } = makeFeed({
      checkSession: vi.fn(async () => Promise.reject(new TypeError('offline'))),
    })
    feed.start()
    connect()
    FakeSocket.last.serverClose(1008)
    await vi.advanceTimersByTimeAsync(1000)
    expect(FakeSocket.instances).toHaveLength(2)
    expect(onSignedOut).not.toHaveBeenCalled()
  })
})

describe('frames', () => {
  it('hands on events and keeps refusals out of them', () => {
    const { feed, events, refused } = makeFeed()
    feed.start()
    const socket = connect()
    socket.receive({
      type: 'turn_start',
      deviceId: 'd1',
      sessionId: 's1',
      turnId: 't1',
      at: '2026-11-12T15:04:12Z',
      data: { trigger: 'wake' },
    })
    socket.receive({ error: 'topic_unknown', detail: 'weather' })
    socket.receive({ error: 'invalid' })
    socket.onmessage?.({ data: 'not json' })

    expect(events).toEqual([
      {
        type: 'turn_start',
        deviceId: 'd1',
        sessionId: 's1',
        turnId: 't1',
        at: '2026-11-12T15:04:12Z',
        data: { trigger: 'wake' },
      },
    ])
    expect(refused).toEqual(['topic_unknown:weather', 'invalid:'])
  })

  it('connects afresh when started again after a stop, as react\'s strict mode does', () => {
    const { feed } = makeFeed()
    feed.want(['tasks'])
    feed.start()
    feed.stop()
    feed.start()
    expect(FakeSocket.instances).toHaveLength(2)
    expect(FakeSocket.instances[0]?.closedWith).toBe(1000)
    expect(connect().frames()).toEqual([
      {
        subscribe: ['tasks'],
      },
    ])
  })

  it('closes the socket on stop and never reconnects', async () => {
    const { feed } = makeFeed()
    feed.start()
    const socket = connect()
    feed.stop()
    expect(socket.closedWith).toBe(1000)
    socket.serverClose(1006)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(FakeSocket.instances).toHaveLength(1)
  })
})
