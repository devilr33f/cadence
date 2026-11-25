import type { QueryClient, QueryKey } from '@tanstack/react-query'
import type { LiveEvent } from './frames'
import type { Topic } from './topics'
import { readsDevice } from '@/lib/devices'
import { isActive, TRANSCRIPT_QUERY } from '@/lib/tasks'
import { text } from './data'
import { TOPIC_OF } from './topics'

// a generated query (its operation id, the _id of its key), narrowed to the event's device when `device` is set; with
// `when`, only an event it holds of makes it stale (a gap still does)
interface Target {
  id: string
  device?: true
  when?: (event: LiveEvent) => boolean
}

const MEMORIES: Target = { id: 'listMemories' }
const LEARNING: Target[] = [
  { id: 'listDeviceLearning', device: true },
  MEMORIES,
]
const MCP: Target[] = [
  { id: 'listMcpServers' },
  { id: 'listMcpTools' },
]
const DEVICES: Target = { id: 'listDevices' }
const GROUP: Target = { id: 'getGroup' }
const DEVICE: Target = { id: 'getDevice', device: true }
// what the list and the detail both show: the online flag, the name, the model, the status and when it was last seen
const PRESENCE: Target[] = [
  DEVICES,
  DEVICE,
]

// every device's inbox: its key names the device as a filter, not as the path an event's device would narrow
const INBOX: Target = { id: 'getSubscriptionInbox' }

// event type -> the queries it makes stale. a type left out changes no query: turn_start and transcript, the agent's
// steps (llm_request, llm_text, tool_call, tool_result, mode_switch, budget_spent, memory_recall: a mode switched by
// voice is in-memory server state, no setting) and log_line feed the page's streams only. device_vitals comes every
// round (10-30 s) and feeds no query: a metrics read is about 1 MB a chart, and health reads on its own interval
export const INVALIDATIONS: Readonly<Record<string, readonly Target[]>> = {
  // the server stores a turn before it publishes its end; a stored turn's messages never change
  turn_end: [
    { id: 'listDeviceTurns', device: true },
  ],
  memory_changed: [MEMORIES],
  compaction: [
    { id: 'listDeviceCompactions', device: true },
  ],
  learning_run: LEARNING,
  learning_op: LEARNING,
  mcp_connected: MCP,
  mcp_unavailable: MCP,
  mcp_tools_changed: MCP,
  // a task's own page may key it by its short id or its uuid: every open one is fetched again
  task_event: [
    { id: 'listTasks' },
    { id: 'getTask' },
    { id: 'inspectTask' },
    // a shown transcript is the run's whole text: read again once the run ended, not on each step's event
    {
      id: TRANSCRIPT_QUERY,
      when: event => !isActive({ status: text(event.data, 'status') ?? '' }),
    },
  ],
  // a song the server's music queue moved to changes the queue too, and a video the server plays its choices
  media_state: [
    { id: 'getDeviceMedia', device: true },
    { id: 'getDeviceMusic', device: true },
    { id: 'getDeviceVideo', device: true },
  ],
  device_online: PRESENCE,
  device_offline: PRESENCE,
  device_added: PRESENCE,
  device_renamed: PRESENCE,
  // the level shows in the detail alone; a media sender's slider can send several a second, which the window coalesces
  device_volume: [DEVICE],
  // the list alone: the device's own queries are dropped (forgetDevice), since a read of it would only be refused
  device_deleted: [DEVICES],
  // a signal set, cleared or expired: which speakers are busy and why
  busy_changed: [
    { id: 'getBusy' },
  ],
  // ids, topics and codes only: the inbox is read again for its texts
  subscription_update: [INBOX],
  subscription_inbox: [INBOX],
  // created, deleted, or ended by its next stop
  subscription_changed: [
    { id: 'listSubscriptions' },
  ],
  // created, changed, deleted or a new leader elected: the groups, and the devices, which name their group
  group_changed: [
    { id: 'listGroups' },
    GROUP,
    DEVICES,
    { id: 'getDevice' },
  ],
  // a member's reported sync (state, clock error, queue) shows on the group's page alone; a burst coalesces per window
  group_state: [GROUP],
  // the alarms' state is the detail's alarms capability: a set, a cancel, a ring and its end
  alarms_state: [DEVICE],
  alarm_ringing: [DEVICE],
  alarm_ring_ended: [DEVICE],
  // a word's status and a training's stage show in the list (a device's picker reads the same list); the window
  // coalesces a training's progress, at most one every 2 %
  wakeword_changed: [
    { id: 'listWakeWords' },
  ],
  wakeword_progress: [
    { id: 'listWakeWords' },
  ],
  wakeword_negatives: [
    { id: 'getWakeWordNegatives' },
  ],
}

// partial keys: query keys match by their first element's fields, so { _id } covers every path, query and page
function keyOf(target: Target, deviceId?: string): QueryKey {
  return target.device && deviceId
    ? [
        {
          _id: target.id,
          path: { id: deviceId },
        },
      ]
    : [
        { _id: target.id },
      ]
}

export function queryKeysForEvent(event: LiveEvent): QueryKey[] {
  return (INVALIDATIONS[event.type] ?? [])
    .filter(target => target.when?.(event) ?? true)
    .map(target => keyOf(target, event.deviceId))
}

// what a gap in a topic may have changed beyond its events' own queries: a gap may be the server restarting, which drops
// every device's link (no device event is replayed)
export const GAP_INVALIDATIONS: Readonly<Partial<Record<Topic, readonly Target[]>>> = {
  devices: [
    { id: 'listDevices' },
    { id: 'getDevice' },
  ],
}

// everything the topics' events could have changed, every device's
export function queryKeysForTopics(topics: readonly Topic[]): QueryKey[] {
  const ids = new Set<string>()
  for (const topic of topics) {
    for (const target of GAP_INVALIDATIONS[topic] ?? [])
      ids.add(target.id)
  }
  for (const [type, targets] of Object.entries(INVALIDATIONS)) {
    if (topics.includes(TOPIC_OF[type]!)) {
      for (const target of targets)
        ids.add(target.id)
    }
  }
  return [...ids].map(id => [
    { _id: id },
  ])
}

export const INVALIDATE_WINDOW_MS = 250

export interface Invalidator {
  event: (event: LiveEvent) => void
  topics: (topics: readonly Topic[]) => void
  // a deleted device: what is pending for it is dropped, so no window reads it after its delete
  forget: (deviceId: string) => void
  flush: () => Promise<void>
  stop: () => void
}

// a burst of events (a turn's steps, a task's progress) invalidates each query once per window, and never cancels a
// refetch already on its way; a key whose query is still fetching waits for the next window, since that fetch may have
// read the server before the change and an invalidation now would only join it
export function createInvalidator(queryClient: QueryClient, windowMs = INVALIDATE_WINDOW_MS): Invalidator {
  const pending = new Map<string, QueryKey>()
  let timer: ReturnType<typeof setTimeout> | undefined

  function schedule() {
    if (pending.size > 0 && timer === undefined)
      timer = setTimeout(() => void flush(), windowMs)
  }

  async function flush() {
    clearTimeout(timer)
    timer = undefined
    const due: QueryKey[] = []
    for (const [id, queryKey] of pending) {
      if (queryClient.isFetching({ queryKey }) === 0) {
        due.push(queryKey)
        pending.delete(id)
      }
    }
    schedule()
    await Promise.all(due.map(queryKey => queryClient.invalidateQueries({ queryKey }, { cancelRefetch: false })))
  }

  function add(keys: QueryKey[]) {
    for (const key of keys)
      pending.set(JSON.stringify(key), key)
    schedule()
  }

  return {
    event: event => add(queryKeysForEvent(event)),
    topics: topics => add(queryKeysForTopics(topics)),
    forget(deviceId) {
      for (const [id, queryKey] of pending) {
        if (readsDevice(queryKey, deviceId))
          pending.delete(id)
      }
    },
    flush,
    stop() {
      clearTimeout(timer)
      timer = undefined
      pending.clear()
    },
  }
}
