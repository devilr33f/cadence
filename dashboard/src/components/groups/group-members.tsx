import type { GroupMemberView, GroupView, MediaGroupMemberStatus, MediaGroupStatus } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { Link } from '@tanstack/react-router'
import { DataTable } from '@/components/data/data-table'
import { DeviceStatus } from '@/components/data/device-status'
import { StatusBadge } from '@/components/data/status-badge'
import { CHANNEL_LABELS, outputKind, stateOf } from './group-words'

// what a member's row shows of its sync: its own report, else what the leader reports of it
type Sync = Pick<MediaGroupStatus, 'state' | 'synced' | 'clockErrorUs' | 'queueMs' | 'rejoins'> | MediaGroupMemberStatus

interface MemberRow {
  member: GroupMemberView
  name: string
  leads: boolean
  sync?: Sync
  // its own media.group report's error, and the leader's view of it (a member alone)
  error?: string
  seen?: MediaGroupMemberStatus
}

function rowsOf(group: GroupView, names: ReadonlyMap<string, string>): MemberRow[] {
  const leader = group.members.find(m => m.deviceId === group.leaderId)
  return group.members.map(member => ({
    member,
    name: names.get(member.deviceId) ?? member.deviceId,
    leads: member.deviceId === group.leaderId,
    sync: member.status ?? leader?.status?.members?.find(m => m.deviceId === member.deviceId),
    error: member.status?.error || undefined,
    seen: member.deviceId === group.leaderId
      ? undefined
      : leader?.status?.members?.find(m => m.deviceId === member.deviceId),
  }))
}

function numberCell(value: number | undefined, unit?: string) {
  return value === undefined
    ? <span className="text-muted-foreground">—</span>
    : (
        <span className="tabular-nums">
          {unit
            ? `${value} ${unit}`
            : value}
        </span>
      )
}

function trimCell(member: GroupMemberView) {
  const trims = member.trims ?? []
  const output = member.status?.output
  const active = trims.find(t => t.outputId === output?.id)?.trimUs ?? member.trimUs
  if (trims.length === 0)
    return numberCell(member.trimUs / 1000, 'ms')
  return (
    <span className="flex flex-col items-end tabular-nums">
      <span>
        {active / 1000}
        {' '}
        ms
      </span>
      {trims.map(trim => (
        <span key={trim.outputId} className="text-xs text-muted-foreground">
          {trim.outputId}
          {' '}
          {trim.trimUs / 1000}
          {' '}
          ms
        </span>
      ))}
    </span>
  )
}

const COLUMNS: readonly Column<MemberRow>[] = [
  {
    id: 'device',
    header: 'Device',
    cell: (r) => {
      const output = r.member.status?.output
      return (
        <span className="flex flex-col">
          <span className="flex items-center gap-2">
            <Link to="/devices/$id" params={{ id: r.member.deviceId }} className="font-medium underline-offset-4 hover:underline">
              {r.name}
            </Link>
            {r.leads && <span className="text-xs text-muted-foreground">Leader</span>}
          </span>
          {output && (
            <span className="text-xs text-muted-foreground tabular-nums">
              {outputKind(output.kind)}
              {output.delayHintUs !== undefined && ` · ${output.delayHintUs / 1000} ms reported`}
            </span>
          )}
        </span>
      )
    },
  },
  {
    id: 'channel',
    header: 'Channel',
    cell: r => CHANNEL_LABELS[r.member.channel],
  },
  {
    id: 'trim',
    header: 'Trim',
    align: 'end',
    // the active output's calibration, with every output's stored trim below it
    cell: r => trimCell(r.member),
    className: 'max-md:hidden',
  },
  {
    id: 'online',
    header: 'Link',
    // the device's link to the server, then its link in the group: its own error, else the leader not reaching it
    cell: (r) => {
      if (!r.member.online)
        return <DeviceStatus status="offline" />
      if (r.error)
        return <StatusBadge tone="warning" label={`Group error (${r.error})`} />
      if (r.seen && !r.seen.connected)
        return <StatusBadge tone="warning" label="Not connected to the leader" />
      return <DeviceStatus status="online" />
    },
  },
  {
    id: 'state',
    header: 'State',
    cell: (r) => {
      if (!r.sync)
        return <span className="text-muted-foreground">Not reported</span>
      const { label, tone } = stateOf(r.sync.state)
      return <StatusBadge tone={tone} label={label} />
    },
  },
  {
    id: 'synced',
    header: 'Synced',
    cell: (r) => {
      if (!r.sync)
        return <span className="text-muted-foreground">—</span>
      return r.sync.synced
        ? <StatusBadge tone="success" label="Yes" />
        : <StatusBadge tone="neutral" label="No" />
    },
  },
  {
    id: 'clock',
    header: 'Clock error',
    align: 'end',
    cell: r => numberCell(r.sync?.clockErrorUs, 'µs'),
  },
  {
    id: 'queue',
    header: 'Queue',
    align: 'end',
    cell: r => numberCell(r.sync?.queueMs, 'ms'),
    className: 'max-lg:hidden',
  },
  {
    id: 'rejoins',
    header: 'Rejoins',
    align: 'end',
    cell: r => numberCell(r.sync?.rejoins),
    className: 'max-lg:hidden',
  },
]

// each member in the group's order, with what it last reported of its sync (group_state keeps it current)
export function GroupMembers({ group, names }: { group: GroupView, names: ReadonlyMap<string, string> }) {
  return (
    <DataTable
      caption="Members"
      columns={COLUMNS}
      rows={rowsOf(group, names)}
      rowKey={r => r.member.deviceId}
      empty={<p className="p-4 text-sm text-muted-foreground">No members.</p>}
    />
  )
}
