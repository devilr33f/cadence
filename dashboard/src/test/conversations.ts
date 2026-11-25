import type { TurnDetail, TurnPage, TurnView } from '@/api/gen'

// a device's turns as list-device-turns answers them, newest first
export const WAKE_TURN: TurnView = {
  turnId: '0193a71e-0000-7000-8000-0000000000a3',
  at: '2026-11-12T15:04:12Z',
  trigger: 'wake',
  mode: 'fast',
  user: 'turn off <b>the</b> kitchen light',
  reply: 'Done, the light is off.',
  tools: [
    { name: 'ha_control' },
  ],
  tokens: { prompt: 1200, completion: 34 },
}

export const EVENT_TURN: TurnView = {
  turnId: '0193a71e-0000-7000-8000-0000000000a2',
  at: '2026-11-12T15:01:00Z',
  trigger: 'event',
  mode: 'fast',
  user: 'Task backup finished: 3 files copied.',
  reply: 'Your backup has finished.',
  tools: [],
}

export const FAILED_TOOL_TURN: TurnView = {
  turnId: '0193a71e-0000-7000-8000-0000000000a1',
  at: '2026-11-12T14:58:30Z',
  trigger: 'button',
  mode: 'smart',
  user: `please ${'very '.repeat(60)}carefully lock the door`,
  tools: [
    { name: 'ha_control', code: 'not_found' },
    { name: 'device_volume' },
  ],
  tokens: { prompt: 5400, completion: 210 },
}

// stored before turns recorded their trigger, mode and tokens
export const OLD_TURN: TurnView = {
  turnId: '0193a71e-0000-7000-8000-0000000000a0',
  at: '2026-11-11T09:00:00Z',
  user: 'hello',
  tools: [],
}

export const FOLLOW_UP_TURN: TurnView = {
  turnId: '0193a71e-0000-7000-8000-00000000009f',
  at: '2026-11-11T08:59:40Z',
  trigger: 'follow_up',
  user: 'and tomorrow?',
  reply: 'Rain.',
  tools: [],
}

export const FIRST_PAGE: TurnPage = {
  turns: [WAKE_TURN, EVENT_TURN, FAILED_TOOL_TURN],
  next: 'cursor-1',
}

export const LAST_PAGE: TurnPage = {
  turns: [OLD_TURN, FOLLOW_UP_TURN],
}

// get-device-turn's messages as stored: a tool call and its result, text a page must never read as markup
export const WAKE_DETAIL: TurnDetail = {
  turnId: WAKE_TURN.turnId,
  at: WAKE_TURN.at,
  trigger: 'wake',
  mode: 'fast',
  tokens: { prompt: 1200, completion: 34 },
  skills: ['home'],
  messages: [
    { seq: 1, role: 'user', content: 'turn off <b>the</b> kitchen light' },
    {
      seq: 2,
      role: 'assistant',
      content: '',
      toolCalls: [
        {
          id: 'call-1',
          name: 'ha_control',
          arguments: { entity: 'light.kitchen', action: 'turn_off' },
        },
      ],
    },
    {
      seq: 3,
      role: 'tool',
      toolCallId: 'call-1',
      content: '{"ok":true,"note":"<img src=x onerror=alert(1)>"}',
    },
    { seq: 4, role: 'assistant', content: 'Done, the light is off.' },
  ],
}

// a call whose result carried an error code, which get-device-turn reads as the list does
export const FAILED_TOOL_DETAIL: TurnDetail = {
  turnId: FAILED_TOOL_TURN.turnId,
  at: FAILED_TOOL_TURN.at,
  trigger: 'button',
  mode: 'smart',
  tokens: { prompt: 5400, completion: 210 },
  messages: [
    { seq: 1, role: 'user', content: 'lock the door' },
    {
      seq: 2,
      role: 'assistant',
      content: '',
      toolCalls: [
        {
          id: 'call-2',
          name: 'ha_control',
          arguments: { entity: 'lock.front', action: 'lock' },
        },
      ],
    },
    {
      seq: 3,
      role: 'tool',
      toolCallId: 'call-2',
      code: 'not_found',
      content: '{"error":"not_found"}',
    },
    { seq: 4, role: 'assistant', content: 'I could not find that lock.' },
  ],
}

// an llm gateway timeout, as a failed turn's list row and detail expose it
export const FAILED_LLM_TURN: TurnView = {
  turnId: '0193a71e-0000-7000-8000-0000000000aa',
  at: '2026-11-12T15:06:00Z',
  trigger: 'wake',
  mode: 'fast',
  user: 'what is on television?',
  tools: [],
  failure: {
    code: 'timeout',
    stage: 'llm',
    step: 1,
    provider: 'gateway-main',
    model: 'vendor/model-x',
    waitedMs: 10001,
    limitMs: 10000,
  },
}

export const FAILED_LLM_DETAIL: TurnDetail = {
  turnId: FAILED_LLM_TURN.turnId,
  at: FAILED_LLM_TURN.at,
  trigger: 'wake',
  mode: 'fast',
  failure: FAILED_LLM_TURN.failure,
  messages: [
    { seq: 0, role: 'user', content: 'what is on television?' },
  ],
}

export const EVENT_DETAIL: TurnDetail = {
  turnId: EVENT_TURN.turnId,
  at: EVENT_TURN.at,
  trigger: 'event',
  mode: 'fast',
  messages: [
    { seq: 1, role: 'user', content: 'Task backup finished: 3 files copied.' },
    { seq: 2, role: 'assistant', content: 'Your backup has finished.' },
  ],
}
