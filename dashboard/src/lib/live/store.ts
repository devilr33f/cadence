import type { BoundedBuffer } from './buffer'
import type { ResyncCause } from './feed'
import type { LiveEvent } from './frames'
import { createBoundedBuffer } from './buffer'

export const TIMELINE_LIMIT = 500
export const LOG_LIMIT = 2000

// the events a timeline shows: turns, the agent's visible steps, tasks and media; the rest only invalidate queries
export const TIMELINE_TYPES: ReadonlySet<string> = new Set([
  'turn_start',
  'transcript',
  'turn_end',
  'assistant_speech',
  'llm_text',
  'tool_call',
  'tool_result',
  'tool_card',
  'mode_switch',
  'budget_spent',
  'task_event',
  'media_state',
])

// an event as a stream keeps it: numbered on arrival, and timed by the server or else by the page
export interface StreamEvent extends LiveEvent {
  seq: number
  at: string
}

// what the log tail keeps: the lines, the feed's count of lines past its 50 a second it held back, and a mark where
// lines may be missing (the tail's own: log_gap)
const LOG_TYPES: ReadonlySet<string> = new Set(['log_line', 'log_line_dropped'])

export interface LiveStore {
  timeline: BoundedBuffer<StreamEvent>
  logs: BoundedBuffer<StreamEvent>
  record: (event: LiveEvent) => void
  logGap: (cause: Exclude<ResyncCause, 'joined'>) => void
}

// what a media session says on a timeline: its state and track; a seek or a volume step changes neither
function mediaKey(data: unknown): string {
  const read = (value: unknown, key: string) => typeof value === 'object' && value !== null
    ? (value as Record<string, unknown>)[key]
    : undefined
  const metadata = read(data, 'metadata')
  return JSON.stringify([
    read(data, 'state'),
    read(data, 'sessionId'),
    read(data, 'source'),
    read(metadata, 'title'),
    read(metadata, 'artist'),
    read(metadata, 'album'),
  ])
}

// a tool call's card as the timeline keeps it: one entry per call, its latest state replacing the last
function callKey(event: LiveEvent): string | undefined {
  const data = event.data
  if (event.type !== 'tool_card' || typeof data !== 'object' || data === null)
    return undefined
  const call = (data as Record<string, unknown>).callId
  return typeof call === 'string'
    ? `${event.turnId ?? ''}/${call}`
    : undefined
}

// the tab's streams, in memory only: they live as long as the signed-in layout and go with it at sign-out
export function createLiveStore(now: () => Date = () => new Date()): LiveStore {
  const timeline = createBoundedBuffer<StreamEvent>(TIMELINE_LIMIT)
  const logs = createBoundedBuffer<StreamEvent>(LOG_LIMIT)
  let seq = 0
  // each device's last media session on the timeline: the device sends one on every change, slider steps included
  const lastMedia = new Map<string, string>()
  return {
    timeline,
    logs,
    record(event) {
      if (event.type === 'media_state') {
        const key = mediaKey(event.data)
        if (lastMedia.get(event.deviceId ?? '') === key)
          return
        lastMedia.set(event.deviceId ?? '', key)
      }
      const buffer = LOG_TYPES.has(event.type)
        ? logs
        : TIMELINE_TYPES.has(event.type)
          ? timeline
          : undefined
      const item = { ...event, seq: seq++, at: event.at ?? now().toISOString() }
      const call = callKey(event)
      if (call !== undefined && buffer === timeline) {
        timeline.upsert(item, old => callKey(old) === call)
        return
      }
      buffer?.push(item)
    },
    logGap(cause) {
      logs.push({
        type: 'log_gap',
        data: { cause },
        seq: seq++,
        at: now().toISOString(),
      })
    },
  }
}
