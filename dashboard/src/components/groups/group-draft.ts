import type { GroupMemberRequest, GroupRequest, GroupView } from '@/api/gen'
import { parseNumber } from '@/components/forms/parse-number'

export type GroupKind = GroupView['kind']
export type Channel = NonNullable<GroupMemberRequest['channel']>

export const CHANNELS: readonly Channel[] = ['both', 'left', 'right', 'mono']

// the server's bounds (pkg/capschema/group.go, server/internal/groups), which it checks again
export const LATENCY_MIN_MS = 200
export const LATENCY_MAX_MS = 5000
export const LATENCY_DEFAULT_MS = 1000
export const TRIM_MIN_MS = -500
export const TRIM_MAX_MS = 500
export const BITRATE_MIN_KBPS = 64
export const BITRATE_MAX_KBPS = 510
export const WAIT_MAX_MS = 10000
export const MIN_MEMBERS = 2
export const MAX_MEMBERS = 8
const MAX_NAME_CHARS = 64
const HIDDEN = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u

// a member as the form edits it, its trim as typed (empty is 0). a calibration's trim is finer than a millisecond: the
// form shows it rounded and keeps the microseconds (keptUs) while the typed trim is still the one it showed (keptText)
export interface MemberDraft {
  deviceId: string
  channel: Channel
  trimMs: string
  keptUs?: number
  keptText?: string
}

// a group as the form edits it: a group's members (in the order they were picked, which the server elects a leader
// by) apart from a pair's two speakers, so switching the kind and back keeps both; the numbers as typed, where an
// empty bitrate or wait keeps the server's default
export interface GroupDraft {
  name: string
  kind: GroupKind
  latencyMs: string
  bitrateKbps: string
  waitMs: string
  pcm: boolean
  // the leader tunes these itself: latency starts at latencyMs, bitrate and wait stay under theirs
  autoLatency: boolean
  autoBitrate: boolean
  autoWait: boolean
  // a video on one of its screens opens on the others, muted, in step with the group's sound
  followVideo: boolean
  // ultrasonic live tracking is edited on the group page, but a whole-group edit must preserve it
  trackingEnabled: boolean
  preferredLeader: string
  members: MemberDraft[]
  left: MemberDraft
  right: MemberDraft
}

// an error's key is the field's json pointer under /body, as the server's refusals name them (members/0/trimMs); a
// pair's left speaker is members/0 and its right members/1, as the request sends them
export type GroupErrors = Partial<Record<string, string>>

export function memberOf(deviceId: string, channel: Channel = 'both'): MemberDraft {
  return { deviceId, channel, trimMs: '' }
}

export const NEW_GROUP: GroupDraft = {
  name: '',
  kind: 'group',
  latencyMs: String(LATENCY_DEFAULT_MS),
  bitrateKbps: '',
  waitMs: '',
  pcm: false,
  autoLatency: true,
  autoBitrate: true,
  autoWait: true,
  followVideo: true,
  trackingEnabled: false,
  preferredLeader: '',
  members: [],
  left: memberOf('', 'left'),
  right: memberOf('', 'right'),
}

function trimText(trimMs: number): string {
  return trimMs === 0
    ? ''
    : String(trimMs)
}

export function draftOf(group: GroupView): GroupDraft {
  const members = group.members.map((m): MemberDraft => ({
    deviceId: m.deviceId,
    channel: m.channel,
    trimMs: trimText(m.trimMs),
    ...(m.trimUs !== m.trimMs * 1000 && {
      keptUs: m.trimUs,
      keptText: trimText(m.trimMs),
    }),
  }))
  const pair = group.kind === 'stereo_pair'
  const left = pair && members.find(m => m.channel === 'left')
  const right = pair && members.find(m => m.channel === 'right')
  return {
    name: group.name,
    kind: group.kind,
    latencyMs: String(group.latencyMs),
    bitrateKbps: String(group.bitrateKbps),
    waitMs: String(group.waitMs),
    pcm: group.pcm,
    autoLatency: group.autoLatency,
    autoBitrate: group.autoBitrate,
    autoWait: group.autoWait,
    followVideo: group.followVideo,
    trackingEnabled: group.trackingEnabled,
    preferredLeader: group.preferredLeader,
    members: pair
      ? []
      : members,
    left: left || memberOf('', 'left'),
    right: right || memberOf('', 'right'),
  }
}

// the members the request sends: a pair's left first, then its right
export function membersOf(draft: GroupDraft): MemberDraft[] {
  return draft.kind === 'stereo_pair'
    ? [
        { ...draft.left, channel: 'left' },
        { ...draft.right, channel: 'right' },
      ]
    : draft.members
}

function numberOf(text: string): number {
  return Number(text.trim())
}

// the whole group, as an update replaces it; a preferred leader no longer among the members is dropped
export function requestOf(draft: GroupDraft): GroupRequest {
  const members = membersOf(draft)
  const leads = draft.preferredLeader !== '' && members.some(m => m.deviceId === draft.preferredLeader)
  return {
    name: draft.name.trim(),
    kind: draft.kind,
    latencyMs: numberOf(draft.latencyMs),
    ...(draft.bitrateKbps.trim() !== '' && {
      bitrateKbps: numberOf(draft.bitrateKbps),
    }),
    ...(draft.waitMs.trim() !== '' && {
      waitMs: numberOf(draft.waitMs),
    }),
    pcm: draft.pcm,
    autoLatency: draft.autoLatency,
    autoBitrate: draft.autoBitrate,
    autoWait: draft.autoWait,
    followVideo: draft.followVideo,
    ...(draft.trackingEnabled && { trackingEnabled: true }),
    ...(leads && { preferredLeader: draft.preferredLeader }),
    members: members.map(m => ({
      deviceId: m.deviceId,
      channel: m.channel,
      trimMs: m.trimMs.trim() === ''
        ? 0
        : numberOf(m.trimMs),
      ...(m.keptUs !== undefined && m.trimMs.trim() === m.keptText && { trimUs: m.keptUs }),
    })),
  }
}

function boundError(text: string, min: number, max: number, required: boolean): string | undefined {
  const parsed = parseNumber(text, 'integer')
  if (parsed.error)
    return parsed.error
  if (parsed.value === undefined)
    return required ? 'Required.' : undefined
  if (parsed.value < min)
    return `At least ${min}.`
  if (parsed.value > max)
    return `At most ${max}.`
  return undefined
}

// what the server would refuse before it is sent; `taken` are the other groups' names (the server compares them in
// any case)
export function checkDraft(draft: GroupDraft, taken: readonly string[]): GroupErrors {
  const found: GroupErrors = {}
  const name = draft.name.trim()
  if (name === '')
    found.name = 'Required.'
  else if ([...name].length > MAX_NAME_CHARS)
    found.name = `At most ${MAX_NAME_CHARS} characters.`
  else if (HIDDEN.test(name))
    found.name = 'No control or format characters.'
  else if (taken.some(t => t.toLowerCase() === name.toLowerCase()))
    found.name = 'Another group has this name.'

  const bounds: [keyof GroupDraft & string, string, number, number, boolean][] = [
    [
      'latencyMs',
      draft.latencyMs,
      LATENCY_MIN_MS,
      LATENCY_MAX_MS,
      true,
    ],
    [
      'bitrateKbps',
      draft.bitrateKbps,
      BITRATE_MIN_KBPS,
      BITRATE_MAX_KBPS,
      false,
    ],
    [
      'waitMs',
      draft.waitMs,
      0,
      WAIT_MAX_MS,
      false,
    ],
  ]
  for (const [field, text, min, max, required] of bounds) {
    const error = boundError(text, min, max, required)
    if (error)
      found[field] = error
  }

  if (draft.kind === 'stereo_pair') {
    if (draft.left.deviceId === '')
      found['members/0/deviceId'] = 'Pick the left speaker.'
    if (draft.right.deviceId === '')
      found['members/1/deviceId'] = 'Pick the right speaker.'
    else if (draft.right.deviceId === draft.left.deviceId)
      found['members/1/deviceId'] = 'Pick another speaker than the left one.'
  }
  else if (draft.members.length < MIN_MEMBERS) {
    found.members = `Pick at least ${MIN_MEMBERS} devices.`
  }
  else if (draft.members.length > MAX_MEMBERS) {
    found.members = `At most ${MAX_MEMBERS} devices.`
  }

  membersOf(draft).forEach((m, i) => {
    const error = boundError(m.trimMs, TRIM_MIN_MS, TRIM_MAX_MS, false)
    if (error)
      found[`members/${i}/trimMs`] = error
  })
  return found
}

// a device picked into a group's members, or taken out of them; a preferred leader taken out leads no more
export function toggleMember(draft: GroupDraft, deviceId: string, picked: boolean): GroupDraft {
  if (picked) {
    return draft.members.some(m => m.deviceId === deviceId)
      ? draft
      : {
          ...draft,
          members: [
            ...draft.members,
            memberOf(deviceId),
          ],
        }
  }
  return {
    ...draft,
    members: draft.members.filter(m => m.deviceId !== deviceId),
    preferredLeader: draft.preferredLeader === deviceId
      ? ''
      : draft.preferredLeader,
  }
}
