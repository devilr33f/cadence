import { expect, it } from 'vitest'
import { createLiveStore, LOG_LIMIT, TIMELINE_LIMIT } from './store'

it('keeps at most 500 timeline events, newest first', () => {
  const store = createLiveStore()
  for (let i = 0; i < 600; i++) {
    store.record({
      type: 'transcript',
      turnId: `t${i}`,
      at: '2026-11-12T15:04:12Z',
      data: { text: `turn ${i}` },
    })
  }

  const timeline = store.timeline.snapshot()
  expect(TIMELINE_LIMIT).toBe(500)
  expect(timeline).toHaveLength(500)
  expect(timeline[0]?.turnId).toBe('t599')
  expect(timeline.at(-1)?.turnId).toBe('t100')
})

it('keeps at most 2000 log lines, newest first', () => {
  const store = createLiveStore()
  for (let i = 0; i < 2100; i++) {
    store.record({
      type: 'log_line',
      at: '2026-11-12T15:04:12Z',
      data: { n: i },
    })
  }

  const logs = store.logs.snapshot()
  expect(LOG_LIMIT).toBe(2000)
  expect(logs).toHaveLength(2000)
  expect(logs[0]?.data).toEqual({ n: 2099 })
  expect(store.timeline.snapshot()).toEqual([])
})

it('keeps the feed\'s count of lines it held back, and a mark where lines may be missing, among the log lines', () => {
  const store = createLiveStore(() => new Date('2026-11-12T15:04:12.500Z'))
  store.record({
    type: 'log_line',
    at: '2026-11-12T15:04:12Z',
    data: { body: 'a' },
  })
  store.record({
    type: 'log_line_dropped',
    data: { count: 312 },
  })
  store.logGap('reconnect')
  expect(store.logs.snapshot().map(e => [e.type, e.data])).toEqual([
    [
      'log_gap',
      { cause: 'reconnect' },
    ],
    [
      'log_line_dropped',
      { count: 312 },
    ],
    [
      'log_line',
      { body: 'a' },
    ],
  ])
  expect(store.logs.snapshot()[0]?.at).toBe('2026-11-12T15:04:12.500Z')
  expect(store.timeline.snapshot()).toEqual([])
})

it('leaves out of the timeline what it does not show, and stamps what came without a time', () => {
  const store = createLiveStore(() => new Date('2026-11-12T15:04:12.500Z'))
  store.record({
    type: 'llm_request',
    data: { step: 1 },
  })
  store.record({
    type: 'memory_recall',
    data: { count: 2 },
  })
  // the server's coming device events and vitals, until a page shows them
  store.record({ type: 'device_online', deviceId: 'd1' })
  store.record({
    type: 'device_vitals',
    deviceId: 'd1',
    data: { cpu: 0.4 },
  })
  store.record({ type: 'media_state', deviceId: 'd1' })
  store.record({
    type: 'task_event',
    data: { short_id: 'ab12', kind: 'done' },
  })

  const timeline = store.timeline.snapshot()
  expect(timeline.map(e => e.type)).toEqual(['task_event', 'media_state'])
  expect(timeline[0]?.at).toBe('2026-11-12T15:04:12.500Z')
  expect(timeline[0]?.seq).toBeTypeOf('number')
})

it('hands react one snapshot until something changes, and tells subscribers when it does', () => {
  const store = createLiveStore()
  let told = 0
  const unsubscribe = store.timeline.subscribe(() => told++)
  const first = store.timeline.snapshot()
  expect(store.timeline.snapshot()).toBe(first)

  store.record({
    type: 'turn_end',
    data: { code: 'ok' },
  })
  expect(told).toBe(1)
  expect(store.timeline.snapshot()).not.toBe(first)

  unsubscribe()
  store.record({
    type: 'turn_end',
    data: { code: 'ok' },
  })
  expect(told).toBe(1)
})

it('keeps a device\'s media state once while it says the same, however often it comes', () => {
  const store = createLiveStore()
  const playing = {
    state: 'playing',
    sessionId: 'm1',
    source: 'soloist',
    metadata: {
      title: 'A',
      artist: 'B',
      album: 'C',
      artworkUrl: '',
    },
    positionMs: 0,
  }
  const media = (deviceId: string, data: object) => store.record({ type: 'media_state', deviceId, data })
  media('d1', playing)
  // a seek or a volume step says nothing new
  media('d1', { ...playing, positionMs: 5000, volume: 40 })
  media('d2', playing)
  media('d1', { ...playing, state: 'paused' })
  media('d1', playing)
  media('d1', {
    ...playing,
    metadata: { ...playing.metadata, title: 'Next' },
  })
  expect(store.timeline.snapshot().map(e => `${e.deviceId} ${(e.data as { state: string, metadata: { title: string } }).state} ${(e.data as { metadata: { title: string } }).metadata.title}`)).toEqual([
    'd1 playing Next',
    'd1 playing A',
    'd1 paused A',
    'd2 playing A',
    'd1 playing A',
  ])
})

it('keeps one timeline entry per tool call card, its latest state in the first one\'s place', () => {
  const store = createLiveStore()
  const card = (status: string, title: string) => store.record({
    type: 'tool_card',
    turnId: 't1',
    at: '2026-11-15T10:00:00Z',
    data: {
      callId: 'c2',
      tool: 'music_play',
      status,
      title,
    },
  })
  card('running', 'Загружаю «Toxicity»')
  store.record({
    type: 'tool_call',
    turnId: 't1',
    at: '2026-11-15T10:00:01Z',
    data: { id: 'c3' },
  })
  card('done', 'Играет «Toxicity»')
  const timeline = store.timeline.snapshot()
  expect(timeline).toHaveLength(2)
  expect(timeline[1]?.type).toBe('tool_card')
  expect((timeline[1]?.data as { title: string }).title).toBe('Играет «Toxicity»')
})
