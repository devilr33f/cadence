import type { GroupView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { Link } from '@tanstack/react-router'
import { GroupActivity } from './group-activity'
import { KIND_LABELS } from './group-words'

export function groupColumns(names: ReadonlyMap<string, string>): readonly Column<GroupView>[] {
  return [
    {
      id: 'name',
      header: 'Name',
      sortValue: g => g.name.toLowerCase(),
      cell: g => (
        <Link to="/groups/$id" params={{ id: g.id }} className="font-medium underline-offset-4 hover:underline">
          {g.name}
        </Link>
      ),
    },
    {
      id: 'kind',
      header: 'Kind',
      cell: g => KIND_LABELS[g.kind],
    },
    {
      id: 'members',
      header: 'Members',
      align: 'end',
      sortValue: g => g.members.length,
      cell: g => <span className="tabular-nums">{g.members.length}</span>,
    },
    {
      id: 'leader',
      header: 'Leader',
      cell: g => g.leaderId === ''
        ? <span className="text-muted-foreground">None</span>
        : names.get(g.leaderId) ?? <span className="font-mono text-xs">{g.leaderId}</span>,
      className: 'max-sm:hidden',
    },
    {
      id: 'state',
      header: 'State',
      cell: g => <GroupActivity group={g} />,
    },
  ]
}
