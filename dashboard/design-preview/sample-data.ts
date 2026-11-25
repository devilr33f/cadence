// synthetic data for the design preview: plausible values, no real household
import type { Generations } from '@/components/data/apply-state'
import type { DeviceState } from '@/components/data/device-status'
import type { LogRecord } from '@/components/data/log-view'

export const TIME_ZONE = 'Asia/Bishkek'

export interface SampleDevice extends Generations {
  id: string
  name: string
  model: string
  status: DeviceState
  lastSeenAt: string
  lastSeen: string
}

export const DEVICES: SampleDevice[] = [
  {
    id: '0193a4c2-7f1e-7c3a-9d52-2b8e41f0a611',
    name: 'Kitchen',
    model: 'yandex-pickle',
    status: 'online',
    lastSeenAt: '2026-11-12T15:04:12Z',
    lastSeen: 'now',
    desiredGen: 14,
    appliedGen: 14,
    appliedError: '',
  },
  {
    id: '0193a4c9-21d0-7b44-8f0e-91c3d27a5b08',
    name: 'Детская',
    model: 'yandex-pickle',
    status: 'updating',
    lastSeenAt: '2026-11-12T15:04:10Z',
    lastSeen: 'now',
    desiredGen: 16,
    appliedGen: 15,
    appliedError: '',
  },
  {
    id: '0193a5e0-4a97-7d1f-a3c6-0e5f8b62d7c4',
    name: 'Bedroom',
    model: 'yandex-midi',
    status: 'offline',
    lastSeenAt: '2026-11-12T12:12:40Z',
    lastSeen: '2 h 52 min ago',
    desiredGen: 9,
    appliedGen: 9,
    appliedError: '',
  },
]

export interface SampleCapability extends Generations {
  id: string
  version: number
  state: string
}

export const CAPABILITIES: SampleCapability[] = [
  {
    id: 'audio.volume',
    version: 1,
    desiredGen: 14,
    appliedGen: 14,
    appliedError: '',
    state: '35 %',
  },
  {
    id: 'leds',
    version: 2,
    desiredGen: 4,
    appliedGen: 3,
    appliedError: '',
    state: 'ring 40 %, clock on',
  },
  {
    id: 'alarms',
    version: 1,
    desiredGen: 7,
    appliedGen: 7,
    appliedError: 'time_zone_unset',
    state: '2 alarms, 0 timers',
  },
  {
    id: 'media.soloist',
    version: 1,
    desiredGen: 2,
    appliedGen: 2,
    appliedError: '',
    state: 'playing',
  },
  {
    id: 'session.followup',
    version: 1,
    desiredGen: 5,
    appliedGen: 5,
    appliedError: '',
    state: 'on, 5 s, 3 in a row',
  },
]

export const LEDS_DIFF = [
  { setting: 'ringBrightness', desired: '60 %', applied: '40 %' },
  { setting: 'clock', desired: 'on', applied: 'on' },
  { setting: 'nightDim', desired: '23:00–07:00', applied: 'off' },
]

export const LOG: LogRecord[] = [
  {
    id: 'l1',
    at: '2026-11-12T15:04:14.212Z',
    severity: 'info',
    source: 'cadenced',
    message: 'session_end',
    attrs: { reason: 'response_end', ms: 6240 },
  },
  {
    id: 'l2',
    at: '2026-11-12T15:04:13.087Z',
    severity: 'debug',
    source: 'audiod',
    message: 'stream_started',
    attrs: { kind: 'reply', rate: 48000 },
  },
  {
    id: 'l3',
    at: '2026-11-12T15:04:12.640Z',
    severity: 'info',
    source: 'cadenced',
    message: 'wake',
    attrs: { trigger: 'voice', score: 0.91 },
  },
  {
    id: 'l4',
    at: '2026-11-12T15:03:58.004Z',
    severity: 'warn',
    source: 'cadenced',
    message: 'config_apply_deferred',
    attrs: { capability: 'leds', gen: 4 },
  },
  {
    id: 'l5',
    at: '2026-11-12T14:41:37.517Z',
    severity: 'error',
    source: 'cadenced',
    message: 'config_apply_failed',
    attrs: { capability: 'alarms', gen: 7, code: 'time_zone_unset' },
  },
  {
    id: 'l6',
    at: '2026-11-12T14:15:00.330Z',
    severity: 'info',
    source: 'kernel',
    message: 'wlan0: link up',
    attrs: { signal: -54 },
  },
]
