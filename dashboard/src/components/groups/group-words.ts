import type { Channel, GroupKind } from './group-draft'
import type { DeviceView, GroupView } from '@/api/gen'
import type { Tone } from '@/components/data/tone'

export const KIND_LABELS: Record<GroupKind, string> = {
  group: 'Group',
  stereo_pair: 'Stereo pair',
}

export const CHANNEL_LABELS: Record<Channel, string> = {
  both: 'Stereo',
  left: 'Left',
  right: 'Right',
  mono: 'Mono',
}

const OUTPUT_LABELS: Readonly<Record<string, string>> = {
  speaker: 'Speaker',
  hdmi: 'HDMI',
  bluetooth: 'Bluetooth',
  other: 'Other output',
}

export function outputKind(kind: string): string {
  return OUTPUT_LABELS[kind] ?? kind
}

// media.group's states (pkg/capschema/group.go): a member that plays in step is the usual, one on its way is info,
// one that lost the group a warning and one that failed a danger
const STATES: Readonly<Record<string, { label: string, tone: Tone }>> = {
  off: { label: 'Off', tone: 'neutral' },
  idle: { label: 'Idle', tone: 'neutral' },
  connecting: { label: 'Connecting', tone: 'info' },
  syncing: { label: 'Syncing', tone: 'info' },
  buffering: { label: 'Buffering', tone: 'info' },
  playing: { label: 'Playing', tone: 'success' },
  rejoining: { label: 'Rejoining', tone: 'warning' },
  failed: { label: 'Failed', tone: 'danger' },
}

// a state the dashboard does not know yet is shown as the device's own token
export function stateOf(state: string): { label: string, tone: Tone } {
  return STATES[state] ?? { label: state, tone: 'neutral' }
}

// a device in another group than this one cannot be picked: the server would refuse it (in_group)
export function elsewhere(device: DeviceView, groupId: string | undefined): string | undefined {
  return device.group && device.group.id !== groupId
    ? device.group.name
    : undefined
}

// a setting the leader may tune: what is in force (the leader's own figure while it reports one, else the group's),
// marked auto when the leader tunes it
export function tuned(value: number, unit: string, auto: boolean): string {
  return auto
    ? `${value} ${unit} · auto`
    : `${value} ${unit}`
}

// who leads: the member that streams (leaderId), and the clock master when another member serves the clock, which is
// when the leader plays a source of its own
export function leaderLine(group: Pick<GroupView, 'leaderId' | 'clockLeaderId'>, nameOf: (deviceId: string) => string): string {
  if (group.leaderId === '')
    return 'None can lead now'
  if (group.clockLeaderId === '' || group.clockLeaderId === group.leaderId)
    return nameOf(group.leaderId)
  return `Playing from ${nameOf(group.leaderId)} · clock: ${nameOf(group.clockLeaderId)}`
}
