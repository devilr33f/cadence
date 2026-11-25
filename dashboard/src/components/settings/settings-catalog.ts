import type { LinkProps } from '@tanstack/react-router'
import type { SettingsReply } from '@/api/gen'
import type { JsonSchema } from '@/components/forms/json-schema'
import type { SettingsScope } from '@/lib/settings'

export type SettingKey = keyof SettingsReply

export type SettingsScopeKind = SettingsScope['kind']

export interface SettingSpec {
  key: SettingKey
  label: string
  // the value's shape for the form renderer; the server validates it and answers settings_invalid
  schema: JsonSchema
  format?: (value: unknown) => string
  // a key only one scope sets: the server refuses it in the other
  only?: SettingsScopeKind
  // the page where what the key names is made
  manage?: { to: LinkProps['to'], label: string }
  // what the server does while no scope sets the key, where it is worth saying
  unset?: string
}

export interface SettingGroup {
  title: string
  settings: readonly SettingSpec[]
}

const INSTANCE: JsonSchema = { type: 'string', description: 'A provider instance\'s name.' }
const STARTUP_INSTANCE: JsonSchema = { type: 'string', description: 'A provider instance\'s name; the server reads it when it starts.' }
const INSTANCE_NAME = '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'
const TEXT: JsonSchema = { type: 'string' }
// the server's settings.MaxPersona, in characters; the table shows the start of it
const PERSONA_MAX = 2000
const PERSONA_PREVIEW = 140
const NAMES: JsonSchema = {
  type: 'array',
  items: { type: 'string' },
  uniqueItems: true,
}
// the keys that name agent modes, whose rows link where the modes are made
const MODES = { to: '/modes', label: 'Manage the modes' } as const
const CLOCK: JsonSchema = { type: 'string', pattern: '^([01][0-9]|2[0-3]):[0-5][0-9]$' }
const BUSY_REASON: JsonSchema = {
  enum: [
    'call',
    'meeting',
    'screenshare',
    'dnd',
  ],
}

function field(value: unknown, key: string): unknown {
  return typeof value === 'object' && value !== null
    ? (value as Record<string, unknown>)[key]
    : undefined
}

function onOff(value: unknown): string {
  return field(value, 'enabled') === true
    ? 'on'
    : 'off'
}

function busyEntitiesText(value: unknown): string {
  if (!Array.isArray(value) || value.length === 0)
    return 'none'
  return value
    .map(item => `${String(field(item, 'entity'))} ${String(field(item, 'state'))} → ${String(field(item, 'reason'))}`)
    .join(', ')
}

function namesOf(value: unknown, key: string): string[] {
  const names = field(value, key)
  return Array.isArray(names)
    ? names.map(String)
    : []
}

function catalogVoiceoversText(value: unknown): string {
  const prefer = namesOf(value, 'prefer')
  const avoid = namesOf(value, 'avoid')
  if (prefer.length === 0 && avoid.length === 0)
    return 'none'
  return [
    prefer.length > 0
      ? `prefer ${prefer.join(' → ')}`
      : '',
    avoid.length > 0
      ? `avoid ${avoid.join(', ')}`
      : '',
  ].filter(Boolean).join('; ')
}

function alarmsText(value: unknown): string {
  const parts: string[] = []
  const add = (key: string, text: (v: unknown) => string) => {
    const v = field(value, key)
    if (v !== undefined)
      parts.push(text(v))
  }
  add('rampSeconds', v => `${String(v)} s ramp`)
  add('rampFrom', v => `from ${String(v)}`)
  add('alarmLimitSeconds', v => `alarms ${String(v)} s`)
  add('timerLimitSeconds', v => `timers ${String(v)} s`)
  add('reminderLimitSeconds', v => `reminders ${String(v)} s`)
  add('snoozeMinutes', v => `snooze ${String(v)} min`)
  add('autoSnooze', v => `${String(v)} auto-snoozes`)
  return parts.length > 0
    ? parts.join(', ')
    : 'defaults'
}

// copies of the server's types and bounds, which no route publishes (a server gap: AGENTS.md, dashboard server gaps)
// every settings key, by what it is about; a scope offers the ones it may set (settingGroups)
const SETTING_GROUPS: readonly SettingGroup[] = [
  {
    title: 'Voice',
    settings: [
      { key: 'stt', label: 'Speech to text', schema: INSTANCE },
      { key: 'tts', label: 'Text to speech', schema: INSTANCE },
      { key: 'voice', label: 'Voice', schema: TEXT },
      {
        key: 'language',
        label: 'Language',
        schema: {
          enum: ['en', 'ru'],
        },
      },
      {
        key: 'timeZone',
        label: 'Time zone',
        schema: { type: 'string', minLength: 1, description: 'An IANA zone, such as Asia/Bishkek.' },
      },
      {
        key: 'followUp',
        label: 'Follow-up',
        schema: {
          type: 'object',
          properties: {
            enabled: { type: 'boolean' },
            windowMs: { type: 'integer', minimum: 2000, maximum: 15000 },
            limit: { type: 'integer', minimum: 1, maximum: 10 },
          },
          required: ['enabled', 'windowMs', 'limit'],
          additionalProperties: false,
        },
        format: v => `${onOff(v)}, ${String(field(v, 'windowMs'))} ms window, ${String(field(v, 'limit'))} in a row`,
      },
      {
        key: 'alarms',
        label: 'Alarms and timers',
        schema: {
          type: 'object',
          description: 'How alarms ramp, how long alarms, timers and reminders ring unstopped, and how they snooze; a key left out is the speaker\'s own default.',
          properties: {
            rampSeconds: { type: 'integer', minimum: 0, maximum: 1800 },
            rampFrom: {
              enum: ['current', 'quiet'],
            },
            alarmLimitSeconds: { type: 'integer', minimum: 60, maximum: 3600 },
            timerLimitSeconds: { type: 'integer', minimum: 10, maximum: 600 },
            reminderLimitSeconds: { type: 'integer', minimum: 10, maximum: 300 },
            snoozeMinutes: { type: 'integer', minimum: 1, maximum: 60 },
            autoSnooze: { type: 'integer', minimum: 0, maximum: 5 },
          },
          additionalProperties: false,
        },
        format: alarmsText,
        unset: 'a 30 s ramp from the level in force, 600 s for an alarm, 60 s for a timer, 9 snooze minutes',
      },
      {
        key: 'errorSound',
        label: 'Error sound',
        schema: {
          enum: ['prompt', 'beep', 'off'],
          description: 'What the speaker plays when a request fails: a spoken prompt in its language, a short beep, or nothing. The lights show it either way.',
        },
      },
      {
        key: 'quietHours',
        label: 'Quiet hours',
        schema: {
          type: 'object',
          properties: {
            enabled: { type: 'boolean' },
            start: CLOCK,
            end: CLOCK,
          },
          required: ['enabled', 'start', 'end'],
          additionalProperties: false,
        },
        format: v => `${onOff(v)}, ${String(field(v, 'start'))}–${String(field(v, 'end'))}`,
      },
      {
        key: 'afterBusy',
        label: 'After busy',
        schema: {
          enum: ['announce', 'inbox'],
          description: 'What becomes of the updates held while the owner was busy: spoken once it ends, or kept for the next time the speaker is woken.',
        },
      },
    ],
  },
  {
    title: 'Assistant',
    settings: [
      { key: 'llm', label: 'Language model', schema: INSTANCE },
      { key: 'model', label: 'Model', schema: TEXT },
      {
        key: 'persona',
        label: 'Personality',
        schema: {
          type: 'string',
          format: 'multiline',
          maxLength: PERSONA_MAX,
          description: 'Your own instructions for the assistant: who listens to this speaker and how to talk with them (a name, warmth, patience, jokes, longer answers). They follow the built-in rules and may change the tone and length of replies, never what the tools do. Set it on a device for that speaker alone.',
        },
        format: v => typeof v === 'string' && v.length > PERSONA_PREVIEW
          ? `${v.slice(0, PERSONA_PREVIEW).trimEnd()}…`
          : String(v),
      },
      {
        key: 'mode',
        label: 'Mode',
        schema: { type: 'string', minLength: 1 },
        manage: MODES,
      },
      {
        key: 'allowedModes',
        label: 'Allowed modes',
        schema: NAMES,
        manage: MODES,
      },
      {
        key: 'modeRevert',
        label: 'Mode revert',
        schema: {
          enum: ['after_idle', 'after_session', 'never'],
        },
      },
      { key: 'tools', label: 'Tools', schema: NAMES },
      { key: 'weather', label: 'Weather', schema: INSTANCE },
      { key: 'weatherLocation', label: 'Weather location', schema: TEXT },
      { key: 'search', label: 'Web search', schema: INSTANCE },
      { key: 'music', label: 'Music search', schema: NAMES },
      { key: 'musicPlayer', label: 'Music player', schema: INSTANCE },
      { key: 'xSearch', label: 'X search', schema: INSTANCE },
      {
        key: 'remoteTargets',
        label: 'Commands from other devices',
        schema: {
          enum: ['allow', 'room', 'deny'],
          description: 'Whether other devices\' turns may drive this one (play on it, move its volume, set its alarms): any device, only devices of its own room, or none.',
        },
      },
    ],
  },
  {
    title: 'Memory',
    settings: [
      { key: 'embed', label: 'Embeddings', schema: INSTANCE },
      {
        key: 'historyTurns',
        label: 'History turns',
        schema: { type: 'integer', minimum: 0, maximum: 50 },
      },
      {
        key: 'compactAfter',
        label: 'Compact after (minutes)',
        schema: { type: 'integer', minimum: 1, maximum: 120 },
      },
      {
        key: 'compactionMaxTokens',
        label: 'Compaction max tokens',
        schema: {
          type: 'integer',
          minimum: 64,
          maximum: 32768,
          description: 'The ceiling on max_tokens when no compaction mode is set. Thinking counts against it even with reasoning off.',
        },
        unset: '8192',
      },
      {
        key: 'compactionMode',
        label: 'Compaction mode',
        schema: {
          type: 'string',
          description: 'An agent mode that already exists. The summary uses its model, reasoning effort, max tokens, first-token timeout and max seconds. This is not the mode the speaker is in. Empty keeps the maintenance model, the token ceiling above, no reasoning and a 60 s attempt.',
        },
        manage: MODES,
        unset: 'maintenance model, no reasoning, 60 s',
      },
      { key: 'maintenanceModel', label: 'Maintenance model', schema: TEXT },
      {
        key: 'memoryMode',
        label: 'Memory mode',
        schema: {
          type: 'string',
          description: 'An agent mode that already exists. The learning call uses its model, reasoning effort, max tokens, first-token timeout and max seconds. Empty uses the compaction call\'s limits.',
        },
        manage: MODES,
        unset: 'the compaction call',
      },
      {
        key: 'memoryScopes',
        label: 'Memory scopes',
        schema: {
          type: 'array',
          items: {
            enum: ['global', 'device'],
          },
          uniqueItems: true,
        },
      },
      {
        key: 'memoryApproval',
        label: 'Memory approval',
        schema: { type: 'boolean' },
      },
    ],
  },
  {
    title: 'Tasks and home',
    settings: [
      {
        key: 'hermesApprovals',
        label: 'Task approvals',
        schema: {
          enum: ['voice', 'dashboard_only'],
        },
      },
      {
        key: 'subscriptionDelivery',
        label: 'Subscription delivery',
        schema: {
          enum: ['speaker', 'agent'],
          description: 'How a subscription made without its own delivery speaks: a fixed sentence, or an agent turn.',
        },
      },
      {
        key: 'subscriptionApprovals',
        label: 'Subscription approvals',
        schema: {
          enum: ['off', 'voice'],
          description: 'Whether a woken session may answer a followed thread\'s approval by voice, once the question was told on the device.',
        },
      },
      {
        key: 'hermes',
        label: 'Hermes',
        schema: STARTUP_INSTANCE,
        only: 'global',
      },
      {
        key: 'homeAssistant',
        label: 'Home Assistant',
        schema: STARTUP_INSTANCE,
        only: 'global',
      },
      {
        key: 'haArea',
        label: 'Home Assistant area',
        schema: { type: 'string', pattern: '^[a-z0-9_]{1,64}$' },
        only: 'device',
      },
      {
        key: 'haSensitive',
        label: 'Sensitive home devices',
        schema: {
          enum: ['woken', 'deny'],
          description: 'What a lock, an alarm or a gate takes by voice: a woken session\'s request, or nothing.',
        },
        only: 'global',
      },
      {
        key: 'haFavorites',
        label: 'Home Assistant favorites',
        schema: {
          type: 'array',
          items: { type: 'string', pattern: '^[a-z][a-z0-9_]*\\.[a-z0-9_]+$', maxLength: 128 },
          maxItems: 32,
          uniqueItems: true,
          description: 'Entity ids every turn sees first, in this order.',
        },
        only: 'global',
      },
      {
        key: 'mcp',
        label: 'MCP servers',
        schema: {
          type: 'array',
          items: { type: 'string', pattern: INSTANCE_NAME },
          maxItems: 32,
          uniqueItems: true,
          description: 'The mcp provider instances whose servers run.',
        },
        only: 'global',
      },
      {
        key: 't3code',
        label: 'T3 Code',
        schema: {
          type: 'array',
          items: { type: 'string', pattern: INSTANCE_NAME },
          maxItems: 32,
          uniqueItems: true,
          description: 'The t3code provider instances whose threads the speakers follow and steer.',
        },
        only: 'global',
        unset: 'T3 Code is off: no thread is followed and the T3 tools are not offered.',
      },
      {
        key: 'catalogSources',
        label: 'Catalog sources',
        schema: {
          type: 'array',
          items: { type: 'string', pattern: INSTANCE_NAME },
          maxItems: 32,
          uniqueItems: true,
          description: 'The torrplayer, torrserver and youtube-music provider instances the TV catalog searches and browses, in the order its rows show them.',
        },
        only: 'global',
        manage: { to: '/providers', label: 'Manage the providers' },
        unset: 'The catalog is empty: the TV finds nothing to watch.',
      },
      {
        key: 'catalogVoiceovers',
        label: 'Catalog voiceovers',
        schema: {
          type: 'object',
          properties: {
            prefer: {
              type: 'array',
              items: { type: 'string', minLength: 1, maxLength: 64 },
              maxItems: 32,
              uniqueItems: true,
              description: 'Dubs and sources wanted, best first (AniLibria, anime365).',
            },
            avoid: {
              type: 'array',
              items: { type: 'string', minLength: 1, maxLength: 64 },
              maxItems: 32,
              uniqueItems: true,
              description: 'Dubs and sources never taken unasked.',
            },
          },
          additionalProperties: false,
          description: 'What the TV plays a title in when it has no remembered choice: the first preferred dub or source it offers; with none, the assistant asks. A name is compared without case or separators, and none may be both preferred and avoided.',
        },
        format: catalogVoiceoversText,
        only: 'global',
        unset: 'none: the assistant asks for a title\'s first voiceover',
      },
      {
        key: 'youtubeVotes',
        label: 'YouTube likes and dislikes',
        schema: {
          type: 'boolean',
          description: 'Adds a YouTube result\'s likes and estimated dislikes from Return YouTube Dislike (returnyoutubedislike.com), which then learns which videos the household looks at: only their ids leave the server. Likes and dislikes: Return YouTube Dislike.',
        },
        only: 'global',
        unset: 'off',
      },
      {
        key: 'busyEntities',
        label: 'Busy entities',
        schema: {
          type: 'array',
          items: {
            type: 'object',
            properties: {
              entity: { type: 'string', pattern: '^[a-z][a-z0-9_]*\\.[a-z0-9_]+$', maxLength: 128 },
              state: { type: 'string', minLength: 1, maxLength: 255 },
              reason: BUSY_REASON,
            },
            required: ['entity', 'state', 'reason'],
            additionalProperties: false,
          },
          maxItems: 32,
          description: 'Home Assistant states that make every speaker busy while they hold, such as a phone\'s call state; each entity and state once.',
        },
        format: busyEntitiesText,
        only: 'global',
      },
    ],
  },
  {
    title: 'Telemetry',
    settings: [
      {
        key: 'logRetentionDays',
        label: 'Log retention (days)',
        schema: {
          type: 'integer',
          minimum: 1,
          maximum: 30,
          description: 'Days of stored lines kept besides today\'s; older days go within the hour. Metrics are kept 24 hours whatever this says.',
        },
        only: 'global',
        unset: '7',
      },
      {
        key: 'telemetryExport',
        label: 'Telemetry export',
        schema: {
          type: 'string',
          pattern: INSTANCE_NAME,
          description: 'The otlp-http provider instance every stored line and point is forwarded to; it applies without a restart.',
        },
        only: 'global',
        manage: { to: '/providers', label: 'Manage the providers' },
        unset: 'off',
      },
      {
        key: 'tracing',
        label: 'Tracing',
        schema: {
          type: 'object',
          properties: {
            enabled: { type: 'boolean' },
            sampleRatio: { type: 'number', minimum: 0, maximum: 1 },
            ringSize: { type: 'integer', minimum: 10, maximum: 5000 },
          },
          required: ['enabled', 'sampleRatio', 'ringSize'],
          additionalProperties: false,
          description: 'Whether the server records spans, the share of new traces it samples (a device\'s or caller\'s trace follows its own) and how many finished traces it keeps in memory; spans also go to the telemetry export. It applies without a restart.',
        },
        format: v => `${onOff(v)}, ${Math.round(Number(field(v, 'sampleRatio')) * 100)}% sampled, ${String(field(v, 'ringSize'))} kept`,
        only: 'global',
        unset: 'on, 100% sampled, 500 kept',
      },
    ],
  },
]

function groupsOf(scope: SettingsScopeKind): readonly SettingGroup[] {
  return SETTING_GROUPS
    .map(group => ({
      ...group,
      settings: group.settings.filter(spec => spec.only === undefined || spec.only === scope),
    }))
    .filter(group => group.settings.length > 0)
}

const BY_SCOPE: Record<SettingsScopeKind, readonly SettingGroup[]> = {
  global: groupsOf('global'),
  device: groupsOf('device'),
}

// the groups a scope shows: the keys it may set, and no group left empty
export function settingGroups(scope: SettingsScopeKind): readonly SettingGroup[] {
  return BY_SCOPE[scope]
}

// a value as the row shows it
export function settingText(spec: SettingSpec, value: unknown): string {
  if (value === undefined)
    return spec.unset ?? '—'
  if (spec.format)
    return spec.format(value)
  if (typeof value === 'boolean')
    return value ? 'on' : 'off'
  if (Array.isArray(value)) {
    return value.length === 0
      ? 'none'
      : value.join(', ')
  }
  if (value === '')
    return 'empty'
  return typeof value === 'object'
    ? JSON.stringify(value)
    : String(value)
}
