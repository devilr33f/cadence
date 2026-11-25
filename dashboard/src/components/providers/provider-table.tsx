import type { ProviderRow } from './provider-rows'
import type { ProviderTest } from './use-provider-tests'
import type { Column } from '@/components/data/data-table'
import { stampText } from '@/lib/format-time'
import { kindLabel } from './provider-modules'
import { rowRank } from './provider-rows'
import { ProviderSecrets } from './provider-secrets'
import { ProviderTestBadge } from './provider-test'

export function providerColumns(timeZone: string | null, tests: Readonly<Record<string, ProviderTest>>): Column<ProviderRow>[] {
  return [
    {
      id: 'name',
      header: 'Name',
      sortValue: r => r.instance.name,
      cell: r => <span className="font-medium">{r.instance.name}</span>,
    },
    {
      id: 'module',
      header: 'Module',
      sortValue: r => r.instance.module,
      cell: r => <span className="font-mono text-xs">{r.instance.module}</span>,
    },
    {
      id: 'kind',
      header: 'Kind',
      sortValue: rowRank,
      cell: r => r.module
        ? kindLabel(r.module.kind)
        : <span className="text-muted-foreground">Unknown</span>,
      className: 'max-sm:hidden',
    },
    {
      id: 'secrets',
      header: 'Secrets',
      cell: r => <ProviderSecrets row={r} />,
    },
    {
      id: 'created',
      header: 'Created',
      sortValue: r => Date.parse(r.instance.createdAt),
      firstSort: 'desc',
      cell: r => <time dateTime={r.instance.createdAt} className="font-semibold">{stampText(r.instance.createdAt, timeZone)}</time>,
      className: 'max-md:hidden',
    },
    {
      id: 'test',
      header: 'Test',
      cell: r => <ProviderTestBadge test={tests[r.instance.id]} />,
    },
  ]
}
