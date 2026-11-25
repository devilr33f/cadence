import type { TokenView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { Stamp } from '@/components/data/stamp'
import { ScopeList } from './token-cells'

export function tokenColumns(timeZone: string | null): Column<TokenView>[] {
  return [
    {
      id: 'name',
      header: 'Name',
      sortValue: t => t.name,
      cell: t => <span className="font-medium">{t.name}</span>,
    },
    {
      id: 'scopes',
      header: 'Scopes',
      cell: t => <ScopeList scopes={t.scopes} mcpTools={t.mcpTools} />,
    },
    {
      id: 'created',
      header: 'Created',
      sortValue: t => Date.parse(t.createdAt),
      firstSort: 'desc',
      cell: t => <Stamp at={t.createdAt} timeZone={timeZone} />,
      className: 'max-md:hidden',
    },
    {
      id: 'used',
      header: 'Last used',
      sortValue: t => t.lastUsedAt === undefined
        ? null
        : Date.parse(t.lastUsedAt),
      firstSort: 'desc',
      cell: t => t.lastUsedAt === undefined
        ? <span className="text-muted-foreground">Never</span>
        : <Stamp at={t.lastUsedAt} timeZone={timeZone} />,
    },
  ]
}
