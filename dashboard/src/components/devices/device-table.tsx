import type { DeviceView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { Link } from '@tanstack/react-router'
import { stampText } from '@/lib/format-time'
import { DeviceStateBadge } from './device-state'

export interface DeviceRow {
  device: DeviceView
  board: string
}

// online, offline, enrolling
function stateOrder(device: Pick<DeviceView, 'status' | 'online'>): number {
  if (device.status !== 'enrolled')
    return 2
  return device.online ? 0 : 1
}

function seenAt(row: DeviceRow): number | null {
  return row.device.lastSeenAt === null
    ? null
    : Date.parse(row.device.lastSeenAt)
}

export function deviceColumns(timeZone: string | null): Column<DeviceRow>[] {
  return [
    {
      id: 'name',
      header: 'Name',
      sortValue: r => r.device.name,
      cell: r => (
        <Link to="/devices/$id" params={{ id: r.device.id }} className="font-medium underline-offset-4 hover:underline">
          {r.device.name}
        </Link>
      ),
    },
    {
      id: 'status',
      header: 'Status',
      sortValue: r => stateOrder(r.device),
      cell: r => <DeviceStateBadge device={r.device} />,
    },
    {
      id: 'room',
      header: 'Room',
      sortValue: r => r.device.room,
      cell: r => r.device.room || <span className="text-muted-foreground">None</span>,
      className: 'max-sm:hidden',
    },
    {
      id: 'board',
      header: 'Board',
      sortValue: r => r.board,
      cell: r => r.board === r.device.model
        ? <span className="font-mono text-xs">{r.board}</span>
        : r.board,
      className: 'max-sm:hidden',
    },
    {
      id: 'seen',
      header: 'Last seen',
      sortValue: seenAt,
      firstSort: 'desc',
      cell: r => r.device.lastSeenAt === null
        ? <span className="text-muted-foreground">Never</span>
        : <time dateTime={r.device.lastSeenAt} className="font-semibold">{stampText(r.device.lastSeenAt, timeZone)}</time>,
    },
  ]
}
