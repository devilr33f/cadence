import type { CapabilityView, Detail } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { ALARMS_CONFIG, CANCEL_ALARM, LIGHTS_CONFIG, SET_ALARM, SET_TIMER, SOLOIST_CONFIG } from '@/test/device-schemas'
import { json, signedIn } from '@/test/fake-api'

export const ID = '0193a71e-55c0-7e2b-b1a4-6f0d93c2e7aa'
export const OTHER = '0193a71e-55c0-7e2b-b1a4-6f0d93c2e7bb'

// a capability as get-device answers it; the secrets it declares are its secrets' names and descriptions, as the
// server builds both from one descriptor
export function capability(over: Partial<CapabilityView> & Pick<CapabilityView, 'id'>): CapabilityView {
  const secrets = over.secrets ?? []
  return {
    version: 1,
    desired: null,
    desiredGen: 0,
    appliedGen: 0,
    appliedError: '',
    state: null,
    available: true,
    configSchema: null,
    stateSchema: null,
    commands: [],
    secrets,
    secretSpecs: secrets.map(({ name, description }) => ({
      name,
      ...(description !== '' && { description }),
    })),
    ...over,
  }
}

// set_alarm and cancel_alarm carry schemas the form cannot show (a root not and oneOf), set_timer one it can
export const ALARMS = capability({
  id: 'alarms',
  desired: { timeZone: 'Asia/Bishkek' },
  desiredGen: 3,
  appliedGen: 3,
  state: { alarms: [], timers: [], ringing: null },
  configSchema: ALARMS_CONFIG,
  commands: [
    { name: 'cancel_alarm', argsSchema: CANCEL_ALARM },
    { name: 'set_alarm', argsSchema: SET_ALARM },
    { name: 'set_timer', argsSchema: SET_TIMER },
  ],
})

export const LIGHTS = capability({
  id: 'display.lights',
  desired: { lights: false },
  desiredGen: 5,
  appliedGen: 4,
  state: { lights: true, clock: true },
  configSchema: LIGHTS_CONFIG,
})

export const SOLOIST = capability({
  id: 'media.soloist',
  desired: { enabled: true },
  desiredGen: 2,
  appliedGen: 2,
  appliedError: 'key_missing',
  state: { state: 'key_missing' },
  configSchema: SOLOIST_CONFIG,
  secrets: [
    { name: 'apiKey', description: 'The Soloist API key', set: false },
  ],
})

// declares no schema: its config and its command are edited as raw json
export const OUTPUT = capability({
  id: 'audio.output',
  state: { level: 30, channels: 1, maxBitrate: 64000 },
  commands: [
    { name: 'set_level', argsSchema: null },
  ],
})

export function detail(over: Partial<Detail> = {}): Detail {
  return {
    id: ID,
    name: 'Kitchen',
    model: 'sim',
    room: '',
    status: 'enrolled',
    online: true,
    lastSeenAt: '2026-11-12T15:04:12Z',
    daemonVersion: 'v-test',
    capabilities: [ALARMS, LIGHTS, SOLOIST, OUTPUT],
    ...over,
  }
}

export const PLAYING = {
  sessionId: 'm1',
  state: 'playing',
  source: 'soloist',
  metadata: {
    title: 'Clair de lune',
    artist: 'Claude Debussy',
    album: 'Suite bergamasque',
    artworkUrl: 'https://i.scdn.example/cover.jpg',
  },
  positionMs: 161_000,
  durationMs: 302_000,
  rate: 1,
  actions: ['pause', 'next', 'previous'],
}

// the server's music queue for a device that never played from it
export const NO_QUEUE = {
  active: false,
  upcoming: [],
  history: [],
  shuffle: false,
  repeat: 'off',
  radio: false,
}

// a device page's server: the detail as `current` holds it, so a test can change what the next read answers
export function devicePage(current: { detail: Detail }, overrides: Record<string, FakeRoute> = {}): Record<string, FakeRoute> {
  return {
    'GET /session': signedIn,
    'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
    'GET /boards': json([
      { id: 'sim', name: 'Simulator' },
    ]),
    [`GET /devices/${ID}`]: () => Response.json(current.detail),
    [`GET /devices/${ID}/media`]: json({ state: 'idle' }),
    'GET /history': json({ items: [] }),
    [`GET /devices/${ID}/music`]: json(NO_QUEUE),
    [`GET /devices/${ID}/settings`]: json({}),
    ...overrides,
  }
}
