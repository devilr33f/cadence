import type { ReactNode } from 'react'
import type { AreaJson, EntityJson } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { HouseIcon } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { useDebounced } from '@/hooks/use-debounced'
import { ENTITIES_LIMIT, entitiesQueryOptions, HA_DOMAINS, HOME_ASSISTANT_REFRESH_MS } from '@/lib/home-assistant'

const SEARCH_PAUSE_MS = 250

// by id, as the server lists them; names and states as home assistant gives them, sanitized by the server and set as text (a free-text state reads
// "(text)")
const COLUMNS: Column<EntityJson>[] = [
  {
    id: 'name',
    header: 'Name',
    sortValue: e => e.name,
    cell: e => <span className="font-medium">{e.name}</span>,
  },
  {
    id: 'id',
    header: 'Entity',
    sortValue: e => e.id,
    cell: e => <span className="font-mono text-xs">{e.id}</span>,
  },
  {
    id: 'domain',
    header: 'Domain',
    sortValue: e => e.domain,
    cell: e => <span className="font-mono text-xs">{e.domain}</span>,
    className: 'max-md:hidden',
  },
  {
    id: 'area',
    header: 'Area',
    sortValue: e => e.areaName || null,
    cell: e => e.areaName || <span className="text-muted-foreground">—</span>,
    className: 'max-lg:hidden',
  },
  {
    id: 'state',
    header: 'State',
    sortValue: e => e.state,
    cell: e => e.state,
  },
]

function Field({ id, label, children }: { id: string, label: string, children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
    </div>
  )
}

// the entities the client sees, narrowed on the server by a search, an area and a domain; the domains offered are home
// assistant's own (HA_DOMAINS) and any a read returned, never just those of the first page
export function HomeAssistantEntities({ areas }: { areas: readonly AreaJson[] }) {
  const api = useApi()
  const baseId = useId()
  const [search, setSearch] = useState('')
  const [area, setArea] = useState('')
  const [domain, setDomain] = useState('')
  const q = useDebounced(search.trim().toLowerCase(), SEARCH_PAUSE_MS)
  const list = useQuery({
    ...entitiesQueryOptions(api, { q, area, domain }),
    placeholderData: keepPreviousData,
    refetchInterval: HOME_ASSISTANT_REFRESH_MS,
  })
  const domains = useMemo(() => [
    ...new Set([
      ...HA_DOMAINS,
      ...(list.data ?? []).map(e => e.domain),
    ]),
  ].sort(), [list.data])
  const id = (field: string) => `${baseId}-${field}`

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-3">
        <Field id={id('search')} label="Search entities">
          <Input id={id('search')} type="search" className="w-64 max-w-full" value={search} placeholder="Name, id or alias" onChange={e => setSearch(e.target.value)} />
        </Field>
        <Field id={id('area')} label="Area">
          <NativeSelect id={id('area')} size="sm" value={area} onChange={e => setArea(e.target.value)}>
            <NativeSelectOption value="">All areas</NativeSelectOption>
            {areas.map(a => <NativeSelectOption key={a.id} value={a.id}>{a.name}</NativeSelectOption>)}
          </NativeSelect>
        </Field>
        <Field id={id('domain')} label="Domain">
          <NativeSelect id={id('domain')} size="sm" value={domain} onChange={e => setDomain(e.target.value)}>
            <NativeSelectOption value="">All domains</NativeSelectOption>
            {domains.map(d => <NativeSelectOption key={d} value={d}>{d}</NativeSelectOption>)}
          </NativeSelect>
        </Field>
      </div>
      <DataTable
        caption="Entities"
        columns={COLUMNS}
        rows={list.data}
        rowKey={e => e.id}
        className="max-h-[60svh]"
        status={list.isError
          ? 'error'
          : list.isPending ? 'loading' : 'ready'}
        error={<ErrorState title="The entities could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
        empty={<EmptyState icon={HouseIcon} title="No entities match" description="Home Assistant has none with this name, area or domain." />}
      />
      {list.data?.length === ENTITIES_LIMIT && (
        <p className="text-sm text-muted-foreground">{`The first ${ENTITIES_LIMIT} are shown: search, or choose an area or a domain, to narrow them.`}</p>
      )}
    </div>
  )
}
