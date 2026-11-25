import type { ReactNode, RefObject } from 'react'
import type { MemoryValues } from './memory-body'
import type { DeviceView, MemoryJson } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import type { MemoryFilter, MemoryKind } from '@/lib/memory'
import { keepPreviousData, useInfiniteQuery } from '@tanstack/react-query'
import { BrainIcon, PinIcon } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { MoreButton } from '@/components/data/more-button'
import { Stamp } from '@/components/data/stamp'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { memoriesQueryOptions, MEMORY_KINDS } from '@/lib/memory'
import { useTimeZone } from '@/lib/time-zone'
import { DeleteMemoryButton, EditMemoryButton } from './memory-actions'
import { GLOBAL } from './memory-body'
import { MemoryText, ScopeTag } from './memory-cells'
import { kindLabel, scopeName } from './memory-words'

const BY_UPDATE = { column: 'updated', direction: 'desc' } as const

function columns(names: ReadonlyMap<string, string>, timeZone: string | null): Column<MemoryJson>[] {
  return [
    {
      id: 'text',
      header: 'Memory',
      cell: m => <MemoryText text={m.text} />,
      className: 'whitespace-normal py-2',
    },
    {
      id: 'kind',
      header: 'Kind',
      sortValue: m => kindLabel(m.kind),
      cell: m => kindLabel(m.kind),
    },
    {
      id: 'scope',
      header: 'Scope',
      sortValue: m => scopeName(m, names),
      cell: m => <ScopeTag memory={m} names={names} />,
    },
    {
      id: 'pinned',
      header: 'Pinned',
      sortValue: m => m.pinned ? 1 : 0,
      cell: m => m.pinned && (
        <span className="inline-flex items-center gap-1">
          <PinIcon aria-hidden="true" className="size-3.5 text-muted-foreground" />
          Pinned
        </span>
      ),
      className: 'max-md:hidden',
    },
    {
      id: 'hits',
      header: 'Uses',
      sortValue: m => m.hits,
      firstSort: 'desc',
      align: 'end',
      cell: m => m.hits,
      className: 'max-md:hidden',
    },
    {
      id: 'updated',
      header: 'Updated',
      sortValue: m => Date.parse(m.updatedAt),
      firstSort: 'desc',
      cell: m => <Stamp at={m.updatedAt} timeZone={timeZone} />,
      className: 'max-lg:hidden',
    },
  ]
}

function filterOf(scope: string, kind: string): MemoryFilter {
  return {
    ...(scope === GLOBAL && { scope: 'global' }),
    ...(scope !== '' && scope !== GLOBAL && { scope: 'device', deviceId: scope }),
    ...(kind !== '' && { kind: kind as MemoryKind }),
  }
}

function Filter({ label, value, onChange, children }: { label: string, value: string, onChange: (value: string) => void, children: ReactNode }) {
  const id = useId()
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <NativeSelect id={id} size="sm" value={value} onChange={e => onChange(e.target.value)}>{children}</NativeSelect>
    </div>
  )
}

// the active memories, narrowed on the server by scope and kind, a page at a time
export function MemoryList({ devices, names, add, finalFocus }: {
  devices: readonly DeviceView[]
  names: ReadonlyMap<string, string>
  // the empty state's own add button
  add: ReactNode
  // where the focus goes once a delete took its row's button away
  finalFocus: RefObject<HTMLElement | null>
}) {
  const api = useApi()
  const timeZone = useTimeZone()
  const [scope, setScope] = useState('')
  const [kind, setKind] = useState('')
  const filtered = scope !== '' || kind !== ''
  // an edited memory stays a row while it still matches the filter
  const stays = (values: MemoryValues) => (scope === '' || values.scope === scope) && (kind === '' || values.kind === kind)
  const list = useInfiniteQuery({
    ...memoriesQueryOptions(api, filterOf(scope, kind)),
    placeholderData: keepPreviousData,
  })
  const rows = useMemo(() => list.data?.pages.flat(), [list.data])
  const cols = useMemo(() => columns(names, timeZone), [names, timeZone])

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap gap-3">
        <Filter label="Scope" value={scope} onChange={setScope}>
          <NativeSelectOption value="">All scopes</NativeSelectOption>
          <NativeSelectOption value={GLOBAL}>Global</NativeSelectOption>
          {devices.map(device => <NativeSelectOption key={device.id} value={device.id}>{device.name}</NativeSelectOption>)}
        </Filter>
        <Filter label="Kind" value={kind} onChange={setKind}>
          <NativeSelectOption value="">All kinds</NativeSelectOption>
          {MEMORY_KINDS.map(k => <NativeSelectOption key={k} value={k}>{kindLabel(k)}</NativeSelectOption>)}
        </Filter>
      </div>
      <DataTable
        caption="Memories"
        columns={cols}
        rows={rows}
        rowKey={m => m.id}
        initialSort={BY_UPDATE}
        status={list.isError
          ? 'error'
          : list.isPending ? 'loading' : 'ready'}
        error={<ErrorState title="The memories could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
        empty={filtered
          ? <EmptyState icon={BrainIcon} title="No memories match" description="Nothing is remembered in this scope and of this kind." />
          : (
              <EmptyState
                icon={BrainIcon}
                title="No memories yet"
                description="The assistant learns them from conversations, or you add one: a name, a preference, a fact about the home."
                action={add}
              />
            )}
        rowActions={m => (
          <div className="flex justify-end gap-1">
            <EditMemoryButton memory={m} devices={devices} stays={stays} finalFocus={finalFocus} />
            <DeleteMemoryButton memory={m} finalFocus={finalFocus} />
          </div>
        )}
      />
      {list.hasNextPage && (
        <MoreButton busy={list.isFetchingNextPage} onMore={() => void list.fetchNextPage()}>Show more memories</MoreButton>
      )}
    </div>
  )
}
