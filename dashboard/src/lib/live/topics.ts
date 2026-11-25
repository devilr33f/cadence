// the live feed's topics, in the order the server lists them (deploy/server/README.md, Live feed topics)
export const TOPICS = [
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
] as const

export type Topic = typeof TOPICS[number]

// each event type's topic, as the server maps them (server/internal/events/topics.go)
export const TOPIC_OF: Readonly<Record<string, Topic>> = {
  turn_start: 'sessions',
  transcript: 'sessions',
  turn_end: 'sessions',
  assistant_speech: 'sessions',
  llm_request: 'agent',
  llm_text: 'agent',
  tool_call: 'agent',
  tool_result: 'agent',
  tool_card: 'agent',
  mode_switch: 'agent',
  skills_loaded: 'agent',
  budget_spent: 'agent',
  memory_recall: 'agent',
  memory_changed: 'agent',
  compaction: 'agent',
  learning_run: 'agent',
  learning_op: 'agent',
  mcp_connected: 'agent',
  mcp_unavailable: 'agent',
  mcp_tools_changed: 'agent',
  task_event: 'tasks',
  device_online: 'devices',
  device_offline: 'devices',
  device_added: 'devices',
  device_renamed: 'devices',
  device_deleted: 'devices',
  device_volume: 'devices',
  media_state: 'media',
  device_vitals: 'vitals',
  log_line: 'logs',
  busy_changed: 'presence',
  subscription_update: 'subscriptions',
  subscription_changed: 'subscriptions',
  subscription_inbox: 'subscriptions',
  group_changed: 'groups',
  group_state: 'groups',
  alarms_state: 'alarms',
  alarm_ringing: 'alarms',
  alarm_ring_ended: 'alarms',
  span_start: 'traces',
  ops_run_started: 'ops',
  ops_step: 'ops',
  ops_run_ended: 'ops',
  span_update: 'traces',
  span_end: 'traces',
  wakeword_changed: 'wakewords',
  wakeword_progress: 'wakewords',
  wakeword_negatives: 'wakewords',
}

// what every page asks for together, each topic once, in the server's order
export function unionTopics(requests: Iterable<readonly Topic[]>): Topic[] {
  const wanted = new Set<Topic>()
  for (const topics of requests) {
    for (const topic of topics)
      wanted.add(topic)
  }
  return TOPICS.filter(topic => wanted.has(topic))
}

export function sameTopics(a: readonly Topic[] | undefined, b: readonly Topic[]): boolean {
  return a !== undefined && a.length === b.length && a.every((topic, i) => topic === b[i])
}
