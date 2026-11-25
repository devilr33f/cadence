import type { StreamEvent } from '@/lib/live/store'
import { describe, expect, it } from 'vitest'
import { turnView } from './assistant-turn'

let seq = 0
function ev(type: string, data: unknown, turnId = 'dash_A'): StreamEvent {
  seq++
  return {
    seq,
    at: '2026-11-16T10:00:00Z',
    type,
    turnId,
    deviceId: 'dev-1',
    data,
  }
}

// the feed's timeline is newest first
function newestFirst(...events: StreamEvent[]): StreamEvent[] {
  return [...events].reverse()
}

describe('a typed turn\'s view', () => {
  it('reads its steps oldest first, its reply, end and speech, and nothing of another turn', () => {
    const events = newestFirst(
      ev('turn_start', { trigger: 'dashboard' }),
      ev('transcript', { text: 'weather?' }),
      ev('llm_text', {
        step: 1,
        text: 'Let me check.',
        interim: true,
        spoken: false,
      }),
      ev('tool_call', {
        step: 1,
        id: 'c1',
        name: 'weather',
        args: '{"days":1}',
      }),
      ev('turn_start', { trigger: 'wake' }, 'other'),
      ev('tool_result', {
        step: 1,
        id: 'c1',
        name: 'weather',
        ok: true,
        result: { t: 5 },
        ms: 12,
      }),
      ev('llm_text', {
        step: 2,
        text: 'It is 5 degrees.',
        interim: false,
        spoken: true,
      }),
      ev('turn_end', {
        code: 'ok',
        silent: false,
        steps: 2,
        toolCalls: 1,
        ms: 2100,
      }),
      ev('assistant_speech', { code: 'busy' }),
    )
    const view = turnView(events, 'dash_A')
    expect(view.steps.map(s => s.text)).toEqual([
      'Wrote, not spoken: «Let me check.»',
      'Called weather',
      'weather answered in 12 ms',
      'Said: «It is 5 degrees.»',
    ])
    expect(view.reply).toBe('It is 5 degrees.')
    expect(view.end).toEqual({
      code: 'ok',
      silent: false,
      steps: 2,
      toolCalls: 1,
      ms: 2100,
    })
    expect(view.speech).toBe('busy')
  })

  it('has no end while the turn runs', () => {
    const view = turnView(newestFirst(ev('turn_start', { trigger: 'dashboard' })), 'dash_A')
    expect(view.end).toBeUndefined()
    expect(view.steps).toEqual([])
  })
})
