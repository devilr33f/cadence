import type { EventKind, TimelineEntry } from '@/components/data/timeline'
import type { StreamEvent } from '@/lib/live/store'
import { count, flag, json, record, text } from '@/lib/live/data'

// the names the feed publishes (the server's triggerNames)
const TRIGGERS: Readonly<Record<string, string>> = {
  wake: 'Woken by voice',
  button: 'Woken by a press',
  follow_up: 'Follow-up',
  event: 'Event turn',
  dashboard: 'Typed in the dashboard',
}

function triggerText(trigger: string | undefined): string {
  return trigger !== undefined && Object.hasOwn(TRIGGERS, trigger)
    ? TRIGGERS[trigger]!
    : 'Turn began'
}

const TASK_KINDS: Readonly<Record<string, string>> = {
  progress: 'made progress',
  waiting_input: 'waits for an answer',
  answered: 'got its answer',
  done: 'done',
  failed: 'failed',
  cancelled: 'cancelled',
  unknown: 'is in an unknown state',
}

interface Stop {
  kind: EventKind
  text: string
  detail?: string
}

function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1
    ? ''
    : 's'}`
}

function turnEnd(data: unknown): Stop {
  const code = text(data, 'code')
  if (code && code !== 'ok')
    return { kind: 'error', text: `Turn failed: ${code}` }
  const parts = [
    plural(count(data, 'steps') ?? 0, 'step'),
    plural(count(data, 'toolCalls') ?? 0, 'tool call'),
    `${((count(data, 'ms') ?? 0) / 1000).toFixed(1)} s`,
  ]
  if (flag(data, 'silent'))
    parts.push('silent')
  return { kind: 'turn', text: `Turn ended: ${parts.join(', ')}` }
}

const MEDIA_STATES: Readonly<Record<string, string>> = {
  playing: 'Playing',
  paused: 'Paused',
  buffering: 'Buffering',
}

// what a media session says on a timeline: its state and the track, never the cover or the source's own ids
export function mediaText(data: unknown): string {
  const state = text(data, 'state') ?? ''
  const label = MEDIA_STATES[state]
  if (!label)
    return 'Media stopped'
  const metadata = record(data, 'metadata')
  const title = text(metadata, 'title')
  const artist = text(metadata, 'artist')
  if (!title)
    return label
  return artist
    ? `${label} «${title}» · ${artist}`
    : `${label} «${title}»`
}

function stop(event: StreamEvent): Stop | undefined {
  const data = event.data
  switch (event.type) {
    case 'turn_start':
      return {
        kind: 'turn',
        text: triggerText(text(data, 'trigger')),
      }
    case 'transcript': {
      const heard = text(data, 'text')
      return heard === undefined
        ? undefined
        : { kind: 'turn', text: `Heard: «${heard}»` }
    }
    case 'llm_text': {
      const said = text(data, 'text')
      if (said === undefined)
        return undefined
      return {
        kind: 'reply',
        text: flag(data, 'spoken')
          ? `Said: «${said}»`
          : `Wrote, not spoken: «${said}»`,
      }
    }
    case 'tool_call':
      return {
        kind: 'tool',
        text: `Called ${text(data, 'name') ?? 'a tool'}`,
        detail: json(data, 'args'),
      }
    case 'tool_card': {
      // a call's card at its latest state (the timeline keeps one per call): its line and how it went
      const title = text(data, 'title') ?? text(data, 'tool') ?? 'A tool'
      const status = text(data, 'status')
      return {
        kind: status === 'failed'
          ? 'error'
          : 'tool',
        text: status === 'running'
          ? `${title}…`
          : title,
        detail: json(data, 'card'),
      }
    }
    case 'tool_result': {
      const name = text(data, 'name') ?? 'A tool'
      return flag(data, 'ok')
        ? {
            kind: 'tool',
            text: `${name} answered in ${count(data, 'ms') ?? 0} ms`,
            detail: json(data, 'result'),
          }
        : {
            kind: 'error',
            text: `${name} failed: ${text(data, 'code') || 'error'}`,
            detail: json(data, 'result'),
          }
    }
    case 'turn_end':
      return turnEnd(data)
    case 'mode_switch':
      return { kind: 'config', text: `Mode ${text(data, 'from') ?? '?'} → ${text(data, 'to') ?? '?'}` }
    case 'budget_spent':
      return { kind: 'turn', text: `Budget spent after ${plural(count(data, 'steps') ?? 0, 'step')}: ${text(data, 'reason') ?? 'limit'}` }
    case 'task_event': {
      const kind = text(data, 'kind') ?? ''
      return {
        kind: kind === 'failed'
          ? 'error'
          : 'task',
        text: `Task ${text(data, 'short_id') ?? '?'} ${TASK_KINDS[kind] ?? 'changed'}`,
      }
    }
    case 'media_state':
      return {
        kind: 'media',
        text: mediaText(data),
      }
    default:
      return undefined
  }
}

// one feed event as a stop on a timeline: the speaker's name (with names, as the overview has them), then what happened;
// transcripts, replies and tool data are shown here only and never leave the page
export function activityEntry(event: StreamEvent, names?: ReadonlyMap<string, string>): TimelineEntry | undefined {
  const described = stop(event)
  if (!described)
    return undefined
  const device = names && event.deviceId && (names.get(event.deviceId) ?? event.deviceId.slice(0, 8))
  return {
    id: String(event.seq),
    at: event.at,
    kind: described.kind,
    text: device
      ? (
          <>
            <span className="font-semibold">{device}</span>
            {` · ${described.text}`}
          </>
        )
      : described.text,
    detail: described.detail && <pre className="font-mono text-xs break-words whitespace-pre-wrap">{described.detail}</pre>,
  }
}
