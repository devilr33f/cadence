import type { TimelineEntry } from '@/components/data/timeline'
import type { StreamEvent } from '@/lib/live/store'
import { activityEntry } from '@/components/activity/activity-entry'
import { count, flag, text } from '@/lib/live/data'

// a typed turn as the page shows it, read from the feed's events under its id
export interface TurnView {
  // the agent's steps, oldest first: what it wrote between calls, the calls and what each answered
  steps: TimelineEntry[]
  // what the turn said, the steps' spoken text in order
  reply?: string
  end?: { code: string, silent: boolean, steps: number, toolCalls: number, ms: number }
  // how the reply went to the speaker: ok, or the announcement's refusal code
  speech?: string
}

// the turn's own input and its end are the page's bubbles, not steps
const NOT_STEPS: ReadonlySet<string> = new Set([
  'turn_start',
  'transcript',
  'turn_end',
  'assistant_speech',
])

// events is the feed's timeline, newest first
export function turnView(events: readonly StreamEvent[], turnId: string): TurnView {
  const view: TurnView = { steps: [] }
  const said: string[] = []
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i]!
    if (event.turnId !== turnId)
      continue
    const data = event.data
    if (event.type === 'turn_end') {
      view.end = {
        code: text(data, 'code') ?? 'ok',
        silent: flag(data, 'silent') ?? false,
        steps: count(data, 'steps') ?? 0,
        toolCalls: count(data, 'toolCalls') ?? 0,
        ms: count(data, 'ms') ?? 0,
      }
      continue
    }
    if (event.type === 'assistant_speech') {
      view.speech = text(data, 'code') ?? 'internal'
      continue
    }
    if (event.type === 'llm_text' && flag(data, 'spoken')) {
      const t = text(data, 'text')
      if (t)
        said.push(t.trim())
    }
    if (NOT_STEPS.has(event.type))
      continue
    const entry = activityEntry(event)
    if (entry)
      view.steps.push(entry)
  }
  if (said.length > 0)
    view.reply = said.join(' ')
  return view
}
