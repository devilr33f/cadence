import type { QueryKey } from '@tanstack/react-query'
import type { LiveEvent } from './frames'
import type { Topic } from './topics'
import { QueryClient } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiClient } from '@/api/client'
import {
  getDeviceMediaOptions,
  getDeviceOptions,
  getDeviceTurnOptions,
  getTaskOptions,
  listDeviceCompactionsOptions,
  listDeviceLearningOptions,
  listDevicesOptions,
  listMcpServersOptions,
  listMcpToolsOptions,
  listMemoriesInfiniteOptions,
  listMemoriesOptions,
  listTasksOptions,
} from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { busyQueryOptions } from '@/lib/busy'
import { turnsQueryOptions } from '@/lib/conversations'
import { groupQueryOptions, groupsQueryOptions } from '@/lib/groups'
import { DEFAULT_WINDOW, deviceMetricsQueryOptions } from '@/lib/metrics'
import { deviceMusicQueryOptions } from '@/lib/music'
import { inboxQueryOptions, subscriptionsQueryOptions } from '@/lib/subscriptions'
import { inspectQueryOptions, transcriptQueryOptions } from '@/lib/tasks'
import { createInvalidator, INVALIDATE_WINDOW_MS, INVALIDATIONS } from './invalidation'
import { TOPIC_OF, TOPICS } from './topics'

const client = createApiClient()

const keys = {
  devices: sharedKey(listDevicesOptions({ client })).queryKey,
  device1: sharedKey(getDeviceOptions({
    client,
    path: { id: 'd1' },
  })).queryKey,
  device2: sharedKey(getDeviceOptions({
    client,
    path: { id: 'd2' },
  })).queryKey,
  metrics1: deviceMetricsQueryOptions(client, 'd1', 'cpu', DEFAULT_WINDOW).queryKey,
  media1: sharedKey(getDeviceMediaOptions({
    client,
    path: { id: 'd1' },
  })).queryKey,
  media2: sharedKey(getDeviceMediaOptions({
    client,
    path: { id: 'd2' },
  })).queryKey,
  music1: deviceMusicQueryOptions(client, 'd1').queryKey,
  compactions1: sharedKey(listDeviceCompactionsOptions({
    client,
    path: { id: 'd1' },
  })).queryKey,
  learning1: sharedKey(listDeviceLearningOptions({
    client,
    path: { id: 'd1' },
    query: { limit: 20 },
  })).queryKey,
  learning2: sharedKey(listDeviceLearningOptions({
    client,
    path: { id: 'd2' },
  })).queryKey,
  memories: sharedKey(listMemoriesOptions({
    client,
    query: { scope: 'global' },
  })).queryKey,
  memoriesPages: listMemoriesInfiniteOptions({ client }).queryKey,
  tasks: sharedKey(listTasksOptions({ client })).queryKey,
  task: sharedKey(getTaskOptions({
    client,
    path: { id: 'ab12' },
  })).queryKey,
  inspect: inspectQueryOptions(client, 'ab12').queryKey,
  transcript: transcriptQueryOptions(client, 'ab12').queryKey,
  turns1: turnsQueryOptions(client, 'd1').queryKey,
  turns2: turnsQueryOptions(client, 'd2').queryKey,
  turn1: sharedKey(getDeviceTurnOptions({
    client,
    path: { id: 'd1', turnId: 't1' },
  })).queryKey,
  mcpServers: sharedKey(listMcpServersOptions({ client })).queryKey,
  busy: busyQueryOptions(client).queryKey,
  subscriptions: subscriptionsQueryOptions(client).queryKey,
  inbox1: inboxQueryOptions(client, 'd1').queryKey,
  inbox2: inboxQueryOptions(client, 'd2').queryKey,
  mcpTools: sharedKey(listMcpToolsOptions({
    client,
    path: { name: 'notes' },
  })).queryKey,
  groups: groupsQueryOptions(client).queryKey,
  group1: groupQueryOptions(client, 'g1').queryKey,
} satisfies Record<string, QueryKey>

type Name = keyof typeof keys

let queryClient: QueryClient

beforeEach(() => {
  queryClient = new QueryClient()
  for (const key of Object.values(keys))
    queryClient.setQueryData(key as QueryKey, {})
})

function invalidated(): Name[] {
  return (Object.keys(keys) as Name[]).filter(name => queryClient.getQueryState(keys[name])?.isInvalidated)
}

async function afterEvent(event: LiveEvent) {
  const invalidator = createInvalidator(queryClient)
  invalidator.event(event)
  await invalidator.flush()
}

async function afterGap(topics: Topic[]) {
  const invalidator = createInvalidator(queryClient)
  invalidator.topics(topics)
  await invalidator.flush()
}

describe('an event invalidates the queries it affects', () => {
  it.each<[string, string | undefined, Name[]]>([
    [
      'media_state',
      'd1',
      ['media1', 'music1'],
    ],
    [
      'task_event',
      'd1',
      ['tasks', 'task', 'inspect'],
    ],
    [
      'memory_changed',
      undefined,
      ['memories', 'memoriesPages'],
    ],
    [
      'compaction',
      'd1',
      ['compactions1'],
    ],
    [
      'learning_run',
      'd1',
      ['learning1', 'memories', 'memoriesPages'],
    ],
    [
      'learning_op',
      'd2',
      ['learning2', 'memories', 'memoriesPages'],
    ],
    // a turn is stored before its end is published; a turn's own messages never change
    [
      'turn_end',
      'd1',
      ['turns1'],
    ],
    [
      'mcp_connected',
      undefined,
      ['mcpServers', 'mcpTools'],
    ],
    [
      'mcp_unavailable',
      undefined,
      ['mcpServers', 'mcpTools'],
    ],
    [
      'mcp_tools_changed',
      undefined,
      ['mcpServers', 'mcpTools'],
    ],
    // the device as the list and its detail have it: online, name, model, status, last seen
    [
      'device_online',
      'd1',
      ['devices', 'device1'],
    ],
    [
      'device_offline',
      'd2',
      ['devices', 'device2'],
    ],
    [
      'device_added',
      'd1',
      ['devices', 'device1'],
    ],
    [
      'device_renamed',
      'd1',
      ['devices', 'device1'],
    ],
    // only the detail shows the level
    [
      'device_volume',
      'd1',
      ['device1'],
    ],
    // the list alone: a read of the deleted device would only be refused
    [
      'device_deleted',
      'd1',
      ['devices'],
    ],
    [
      'busy_changed',
      'd1',
      ['busy'],
    ],
    // an inbox's key names its device as a filter: every inbox is read again
    [
      'subscription_update',
      'd1',
      ['inbox1', 'inbox2'],
    ],
    [
      'subscription_inbox',
      'd1',
      ['inbox1', 'inbox2'],
    ],
    [
      'subscription_changed',
      'd1',
      ['subscriptions'],
    ],
    // the devices name their group
    [
      'group_changed',
      undefined,
      [
        'devices',
        'device1',
        'device2',
        'groups',
        'group1',
      ],
    ],
    // a member's sync shows on the group's page alone
    [
      'group_state',
      'd1',
      ['group1'],
    ],
    // the device's alarms capability, in its detail alone
    [
      'alarms_state',
      'd1',
      ['device1'],
    ],
    [
      'alarm_ringing',
      'd2',
      ['device2'],
    ],
    [
      'alarm_ring_ended',
      'd1',
      ['device1'],
    ],
  ])('%s (device %s)', async (type, deviceId, expected) => {
    await afterEvent({ type, deviceId })
    expect(invalidated()).toEqual(expected)
  })

  it.each([
    'turn_start',
    'transcript',
    'llm_request',
    'llm_text',
    'tool_call',
    'tool_result',
    'mode_switch',
    'budget_spent',
    'memory_recall',
    'log_line',
    'something_new',
    // every round's vitals: a metrics read is about 1 MB a chart, and health reads on its own interval
    'device_vitals',
  ])(
    '%s changes no query',
    async (type) => {
      await afterEvent({ type, deviceId: 'd1' })
      expect(invalidated()).toEqual([])
    },
  )

  // a shown transcript is the run's whole text, read again once the run ended rather than on each step
  it.each([
    [
      'running',
      ['tasks', 'task', 'inspect'],
    ],
    [
      'waiting_input',
      ['tasks', 'task', 'inspect'],
    ],
    [
      'done',
      [
        'tasks',
        'task',
        'inspect',
        'transcript',
      ],
    ],
    [
      'failed',
      [
        'tasks',
        'task',
        'inspect',
        'transcript',
      ],
    ],
    [
      'cancelled',
      [
        'tasks',
        'task',
        'inspect',
        'transcript',
      ],
    ],
  ])('a task_event of a %s task', async (status, expected) => {
    await afterEvent({
      type: 'task_event',
      data: { short_id: 'ab12', status },
    })
    expect(invalidated()).toEqual(expected)
  })

  it('widens a device\'s event without a device to every device', async () => {
    await afterEvent({ type: 'media_state' })
    expect(invalidated()).toEqual(['media1', 'media2', 'music1'])
  })
})

it('after a gap, invalidates everything the subscribed topics could have changed', async () => {
  await afterGap(['media', 'tasks'])
  expect(invalidated()).toEqual([
    'media1',
    'media2',
    'music1',
    'tasks',
    'task',
    'inspect',
    'transcript',
  ])

  queryClient = new QueryClient()
  for (const key of Object.values(keys))
    queryClient.setQueryData(key as QueryKey, {})
  await afterGap(['sessions', 'logs'])
  expect(invalidated()).toEqual(['turns1', 'turns2'])
})

// a type without a topic would drop out of the gap's reads
it('names a topic for every event that invalidates', () => {
  expect(Object.keys(INVALIDATIONS).filter(type => !Object.hasOwn(TOPIC_OF, type))).toEqual([])
})

// a gap may be the server restarting, which drops every device's link
it('after a gap in the devices topic, reads the devices and their details again', async () => {
  await afterGap(['devices'])
  expect(invalidated()).toEqual(['devices', 'device1', 'device2'])
})

it('after a gap in the alarms topic, reads every device\'s detail again', async () => {
  await afterGap(['alarms'])
  expect(invalidated()).toEqual(['device1', 'device2'])
})

it('after a gap in the vitals topic, reads nothing again', async () => {
  await afterGap(['vitals'])
  expect(invalidated()).toEqual([])
})

// server/internal/events/topics.go, as deploy/server/README.md's table lists it
it('maps the devices and vitals events to their topics, in the server\'s order', () => {
  expect(TOPICS).toEqual([
    'devices',
    'sessions',
    'agent',
    'tasks',
    'media',
    'vitals',
    'logs',
    'presence',
    'subscriptions',
    'groups',
    'alarms',
    'traces',
    'ops',
    'wakewords',
  ])
  const devices = [
    'device_online',
    'device_offline',
    'device_added',
    'device_renamed',
    'device_deleted',
    'device_volume',
  ]
  expect(devices.map(type => TOPIC_OF[type])).toEqual(devices.map(() => 'devices'))
  expect(TOPIC_OF.device_vitals).toBe('vitals')
  const alarms = ['alarms_state', 'alarm_ringing', 'alarm_ring_ended']
  expect(alarms.map(type => TOPIC_OF[type])).toEqual(alarms.map(() => 'alarms'))
  const ops = ['ops_run_started', 'ops_step', 'ops_run_ended']
  expect(ops.map(type => TOPIC_OF[type])).toEqual(ops.map(() => 'ops'))
})

it('drops what is pending for a deleted device, so nothing reads it once it is gone', async () => {
  const invalidator = createInvalidator(queryClient)
  invalidator.event({ type: 'device_volume', deviceId: 'd1' })
  invalidator.event({ type: 'media_state', deviceId: 'd1' })
  invalidator.event({ type: 'turn_end', deviceId: 'd1' })
  invalidator.event({ type: 'device_volume', deviceId: 'd2' })
  invalidator.event({ type: 'task_event' })
  invalidator.forget('d1')
  await invalidator.flush()
  expect(invalidated()).toEqual(['device2', 'tasks', 'task', 'inspect'])
})

describe('coalescing', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // a media sender's slider can move the level several times a second
  it('reads a device again once per window however often its volume moved', async () => {
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const invalidator = createInvalidator(queryClient)
    for (let level = 10; level < 30; level++) {
      invalidator.event({
        type: 'device_volume',
        deviceId: 'd1',
        data: { level },
      })
    }
    await vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS)
    expect(invalidate.mock.calls.map(([filters]) => filters?.queryKey)).toEqual([
      [
        {
          _id: 'getDevice',
          path: { id: 'd1' },
        },
      ],
    ])
  })

  it('invalidates each query once per window however many events came, never cancelling a refetch on its way', async () => {
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const invalidator = createInvalidator(queryClient)
    for (let i = 0; i < 50; i++) {
      invalidator.event({ type: 'task_event', deviceId: 'd1' })
      invalidator.event({
        type: 'media_state',
        deviceId: i % 2 === 0
          ? 'd1'
          : 'd2',
      })
    }
    expect(invalidate).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS)
    expect(invalidate.mock.calls.map(([filters]) => filters?.queryKey)).toEqual([
      [
        { _id: 'listTasks' },
      ],
      [
        { _id: 'getTask' },
      ],
      [
        { _id: 'inspectTask' },
      ],
      [
        {
          _id: 'getDeviceMedia',
          path: { id: 'd1' },
        },
      ],
      [
        {
          _id: 'getDeviceMusic',
          path: { id: 'd1' },
        },
      ],
      [
        {
          _id: 'getDeviceVideo',
          path: { id: 'd1' },
        },
      ],
      [
        {
          _id: 'getDeviceMedia',
          path: { id: 'd2' },
        },
      ],
      [
        {
          _id: 'getDeviceMusic',
          path: { id: 'd2' },
        },
      ],
      [
        {
          _id: 'getDeviceVideo',
          path: { id: 'd2' },
        },
      ],
    ])
    expect(invalidate.mock.calls.every(([, options]) => options?.cancelRefetch === false)).toBe(true)

    invalidator.event({ type: 'task_event' })
    await vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS)
    expect(invalidate).toHaveBeenCalledTimes(12)
  })

  it('holds back a key whose query is still fetching, so its invalidation never joins a read from before the change', async () => {
    let release!: () => void
    const slow = new Promise<object>((resolve) => {
      release = () => resolve({})
    })
    void queryClient.fetchQuery({
      queryKey: keys.media1,
      queryFn: () => slow,
    })
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const invalidator = createInvalidator(queryClient)
    invalidator.event({ type: 'media_state', deviceId: 'd1' })
    invalidator.event({ type: 'memory_changed' })

    // the queue's read is not on its way: it goes in the first window
    await vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS)
    expect(invalidate.mock.calls.map(([filters]) => filters?.queryKey)).toEqual([
      [
        {
          _id: 'getDeviceMusic',
          path: { id: 'd1' },
        },
      ],
      [
        {
          _id: 'getDeviceVideo',
          path: { id: 'd1' },
        },
      ],
      [
        { _id: 'listMemories' },
      ],
    ])

    // still fetching at the next window: held again
    await vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS)
    expect(invalidate).toHaveBeenCalledTimes(3)

    release()
    await vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS)
    expect(invalidate.mock.calls.at(-1)?.[0]?.queryKey).toEqual([
      {
        _id: 'getDeviceMedia',
        path: { id: 'd1' },
      },
    ])
    expect(queryClient.getQueryState(keys.media1)?.isInvalidated).toBe(true)
    await vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS * 2)
    expect(invalidate).toHaveBeenCalledTimes(4)
  })

  it('drops what is pending when stopped', async () => {
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const invalidator = createInvalidator(queryClient)
    invalidator.event({ type: 'task_event' })
    invalidator.stop()
    await vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS * 2)
    expect(invalidate).not.toHaveBeenCalled()
  })
})
