import { isObject } from '@/components/forms/json-pointer'

// the alarms capability's state as the device reports it (device/internal/alarms/schemas.go), read tolerantly: a
// malformed entry is dropped, and a list of a kind this page does not know yet is kept as generic items

export interface Alarm {
  id: string
  time: string
  days: string[]
  date?: string
  level?: number
  rampSeconds?: number
  rampFrom?: string
  limitSeconds?: number
  next?: string
}

export interface Timer {
  id: string
  seconds: number
  endsAt: string
}

export interface Reminder {
  id: string
  time?: string
  days: string[]
  date?: string
  at?: string
  label: string
  level?: number
  next?: string
}

export interface OtherItem {
  id: string
  label?: string
  next?: string
}

export interface OtherKind {
  kind: string
  items: OtherItem[]
}

export interface Ref {
  kind: string
  id: string
}

export interface Snooze extends Ref {
  until: string
}

export interface LastRing extends Ref {
  at: string
  stoppedBy: string
}

export interface AlarmsState {
  alarms: Alarm[]
  timers: Timer[]
  reminders: Reminder[]
  others: OtherKind[]
  ringing: Ref | null
  snoozed: Snooze[]
  lastRing: LastRing | null
}

const KNOWN = new Set([
  'alarms',
  'timers',
  'reminders',
  'ringing',
  'snoozed',
  'lastRing',
])

function str(value: unknown): string | undefined {
  return typeof value === 'string' && value !== ''
    ? value
    : undefined
}

function num(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value)
    ? value
    : undefined
}

function strings(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((v): v is string => typeof v === 'string')
    : []
}

// each object of a list that `read` can make sense of
function list<T>(value: unknown, read: (item: Record<string, unknown>) => T | undefined): T[] {
  if (!Array.isArray(value))
    return []
  return value.flatMap((item) => {
    const parsed = isObject(item)
      ? read(item)
      : undefined
    return parsed === undefined
      ? []
      : [parsed]
  })
}

function ref(value: unknown): Ref | null {
  if (!isObject(value))
    return null
  const kind = str(value.kind)
  const id = str(value.id)
  return kind && id
    ? { kind, id }
    : null
}

// a list key's kind: reminders -> reminder
function kindOf(key: string): string {
  return key.endsWith('s') && key.length > 1
    ? key.slice(0, -1)
    : key
}

export function parseAlarmsState(state: unknown): AlarmsState {
  const s = isObject(state)
    ? state
    : {}
  const alarms = list<Alarm>(s.alarms, (a) => {
    const id = str(a.id)
    const time = str(a.time)
    if (!id || !time)
      return undefined
    return {
      id,
      time,
      days: strings(a.days),
      date: str(a.date),
      level: num(a.level),
      rampSeconds: num(a.rampSeconds),
      rampFrom: str(a.rampFrom),
      limitSeconds: num(a.limitSeconds),
      next: str(a.next),
    }
  })
  const timers = list<Timer>(s.timers, (t) => {
    const id = str(t.id)
    const endsAt = str(t.endsAt) ?? str(t.next)
    if (!id || !endsAt)
      return undefined
    return { id, seconds: num(t.seconds) ?? 0, endsAt }
  })
  const reminders = list<Reminder>(s.reminders, (r) => {
    const id = str(r.id)
    if (!id)
      return undefined
    return {
      id,
      time: str(r.time),
      days: strings(r.days),
      date: str(r.date),
      at: str(r.at),
      label: str(r.label) ?? '',
      level: num(r.level),
      next: str(r.next),
    }
  })
  const others: OtherKind[] = Object.entries(s)
    .filter(([key, value]) => !KNOWN.has(key) && Array.isArray(value))
    .map(([key, value]) => ({
      kind: kindOf(key),
      items: list<OtherItem>(value, (o) => {
        const id = str(o.id)
        return id
          ? { id, label: str(o.label) ?? str(o.name), next: str(o.next) ?? str(o.endsAt) }
          : undefined
      }),
    }))
    .filter(other => other.items.length > 0)
  const snoozed = list<Snooze>(s.snoozed, (z) => {
    const r = ref(z)
    const until = str(z.until)
    return r && until
      ? { ...r, until }
      : undefined
  })
  let lastRing: LastRing | null = null
  if (isObject(s.lastRing)) {
    const r = ref(s.lastRing)
    const at = str(s.lastRing.at)
    if (r && at)
      lastRing = { ...r, at, stoppedBy: str(s.lastRing.stoppedBy) ?? '' }
  }
  return {
    alarms,
    timers,
    reminders,
    others,
    ringing: ref(s.ringing),
    snoozed,
    lastRing,
  }
}

export interface NextUp extends Ref {
  at: string
  label?: string
}

// the soonest thing that will sound or speak: an alarm's, a reminder's or another kind's next, a timer's end
export function nextUp(state: AlarmsState): NextUp | undefined {
  const candidates: NextUp[] = [
    ...state.alarms.flatMap(a => a.next
      ? [
          { kind: 'alarm', id: a.id, at: a.next },
        ]
      : []),
    ...state.timers.map(t => ({ kind: 'timer', id: t.id, at: t.endsAt })),
    ...state.reminders.flatMap(r => r.next
      ? [
          {
            kind: 'reminder',
            id: r.id,
            at: r.next,
            label: r.label || undefined,
          },
        ]
      : []),
    ...state.others.flatMap(o => o.items.flatMap(i => i.next
      ? [
          {
            kind: o.kind,
            id: i.id,
            at: i.next,
            label: i.label,
          },
        ]
      : [])),
  ].filter(c => !Number.isNaN(Date.parse(c.at)))
  return candidates.reduce<NextUp | undefined>(
    (soonest, c) => soonest === undefined || Date.parse(c.at) < Date.parse(soonest.at)
      ? c
      : soonest,
    undefined,
  )
}

export function snoozeOf(state: AlarmsState, kind: string, id: string): Snooze | undefined {
  return state.snoozed.find(z => z.kind === kind && z.id === id)
}

const DAY_ORDER = [
  'mon',
  'tue',
  'wed',
  'thu',
  'fri',
  'sat',
  'sun',
]
const WEEKDAYS = DAY_ORDER.slice(0, 5)
const WEEKEND = DAY_ORDER.slice(5)

function sameDays(days: readonly string[], set: readonly string[]): boolean {
  return days.length === set.length && set.every(day => days.includes(day))
}

function dateText(date: string): string {
  const at = Date.parse(`${date}T00:00:00Z`)
  if (Number.isNaN(at))
    return date
  return new Intl.DateTimeFormat('en-GB', {
    day: 'numeric',
    month: 'short',
    year: 'numeric',
    timeZone: 'UTC',
  })
    .format(new Date(at))
}

// how an alarm or a reminder repeats: its days in words, else its one date, else once
export function repeatText(days: readonly string[], date?: string): string {
  const lower = [
    ...new Set(days.map(day => day.toLowerCase())),
  ]
  if (lower.length > 0) {
    if (sameDays(lower, DAY_ORDER))
      return 'Every day'
    if (sameDays(lower, WEEKDAYS))
      return 'Weekdays'
    if (sameDays(lower, WEEKEND))
      return 'Weekends'
    return lower
      .sort((a, b) => DAY_ORDER.indexOf(a) - DAY_ORDER.indexOf(b))
      .map(day => day.charAt(0).toUpperCase() + day.slice(1))
      .join(', ')
  }
  if (date)
    return dateText(date)
  return 'Once'
}

const STOPPED_BY: Record<string, string> = {
  button: 'by a button',
  volume: 'by a volume press',
  wake: 'by the wake word',
  word: 'by a stop word',
  timeout: 'rang out',
  snooze: 'snoozed',
  remote: 'remotely',
}

export function stoppedByText(stoppedBy: string): string {
  return STOPPED_BY[stoppedBy] ?? (stoppedBy || 'unknown')
}

export function kindText(kind: string): string {
  return kind.charAt(0).toUpperCase() + kind.slice(1)
}
