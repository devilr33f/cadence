import type { BusyView, Entry, InboxItem, Instance, Subscription } from '@/api/gen'
import { BEDROOM, KITCHEN } from './household'

// outside text the server passes on as it came: the page must show it as text, never as markup
export const HOSTILE_TITLE = 'Fix <b>login</b> <script>alert(1)</script>'
export const HOSTILE_QUESTION = 'Run <img src=x onerror=alert(1)> the migration?'

export const T3: Instance = {
  id: 'p-t3',
  name: 'work',
  module: 't3code',
  config: {},
  createdAt: '2026-11-10T08:00:00Z',
  secrets: {},
}

export const SUBSCRIPTIONS: Subscription[] = [
  {
    id: '11111111-1111-4111-8111-111111111111',
    topic: 't3code:work:*',
    events: [],
    delivery: 'speaker',
    device: KITCHEN,
    until: 'unsubscribed',
    createdBy: 'voice',
    createdAt: '2026-11-13T08:00:00Z',
  },
  {
    id: '22222222-2222-4222-8222-222222222222',
    topic: 't3code:work:thread:th-9',
    events: ['stopped', 'question'],
    delivery: 'mute',
    device: BEDROOM,
    until: 'next_stop',
    createdBy: 'api',
    createdAt: '2026-11-14T08:00:00Z',
  },
]

export const TOP_ENTRIES: Entry[] = [
  { topic: 't3code:work:project:p1:*', kind: 'project', title: 'Cadence' },
  {
    topic: 't3code:work:thread:th-1',
    kind: 'thread',
    status: 'running',
    title: HOSTILE_TITLE,
  },
]

export const PROJECT_ENTRIES: Entry[] = [
  {
    topic: 't3code:work:thread:th-2',
    kind: 'thread',
    status: 'idle',
    title: 'Dashboard pages',
  },
]

export const NOT_BUSY: BusyView = { signals: [], devices: [] }

export const DND: BusyView = {
  signals: [
    {
      source: 'dashboard',
      reason: 'dnd',
      devices: null,
      expiresAt: '2026-11-14T16:00:00Z',
    },
    {
      source: 'ha:binary_sensor.mac_audio_input_in_use',
      reason: 'meeting',
      devices: null,
      expiresAt: null,
    },
  ],
  devices: [
    { deviceId: KITCHEN, reason: 'meeting' },
    { deviceId: BEDROOM, reason: 'meeting' },
  ],
}

export const CLARIFY: InboxItem = {
  id: 3,
  at: '2026-11-14T09:00:00Z',
  delivery: 'speaker',
  device: KITCHEN,
  event: 'question',
  module: 't3code',
  instance: 'work',
  topic: 't3code:work:thread:th-1',
  title: HOSTILE_TITLE,
  state: 'delivered',
  seen: false,
  open: true,
  question: {
    kind: 'clarify',
    token: 'tok-clarify',
    text: HOSTILE_QUESTION,
    options: [
      { id: 'a', label: 'Yes, run it' },
      { id: 'b', label: 'Not yet' },
    ],
    allowCustom: true,
  },
}

export const APPROVAL: InboxItem = {
  id: 4,
  at: '2026-11-14T09:05:00Z',
  delivery: 'speaker',
  device: KITCHEN,
  event: 'approval',
  module: 't3code',
  instance: 'work',
  topic: 't3code:work:thread:th-2',
  title: 'Dashboard pages',
  state: 'delivered',
  seen: true,
  open: true,
  question: {
    kind: 'approval',
    token: 'tok-approval',
    text: 'Write to src/lib/busy.ts',
  },
}

export const STOPPED: InboxItem = {
  id: 1,
  at: '2026-11-14T08:30:00Z',
  delivery: 'speaker',
  device: KITCHEN,
  event: 'stopped',
  module: 't3code',
  instance: 'work',
  topic: 't3code:work:thread:th-1',
  title: HOSTILE_TITLE,
  text: 'Finished <i>the</i> work',
  state: 'pending',
  heldBusy: true,
  seen: false,
}

export const ERRORED: InboxItem = {
  id: 2,
  at: '2026-11-14T08:40:00Z',
  delivery: 'agent',
  device: KITCHEN,
  event: 'error',
  module: 't3code',
  instance: 'work',
  topic: 't3code:work:thread:th-2',
  title: 'Dashboard pages',
  state: 'inbox',
  seen: true,
}

export const INBOX: InboxItem[] = [STOPPED, ERRORED, CLARIFY, APPROVAL]
