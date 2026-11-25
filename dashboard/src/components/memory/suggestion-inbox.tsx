import type { RefObject } from 'react'
import type { DeviceView, MemoryJson } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { useQuery } from '@tanstack/react-query'
import { InboxIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { Stamp } from '@/components/data/stamp'
import { EmptyState, ErrorState } from '@/components/data/states'
import { StatusBadge } from '@/components/data/status-badge'
import { SUGGESTIONS_LIMIT, suggestionsQueryOptions } from '@/lib/memory'
import { useTimeZone } from '@/lib/time-zone'
import { MemoryText, ScopeTag } from './memory-cells'
import { CHANGE_LABELS, changeOf, kindLabel, scopeName } from './memory-words'
import { SuggestionActions } from './suggestion-actions'
import { useReplacedTexts } from './use-replaced-texts'

const NEWEST = { column: 'created', direction: 'desc' } as const

const CHANGE_TONE = { new: 'info', update: 'info', delete: 'warning' } as const

// an update proposal shows beside its new text the active one it would overwrite, so an approval never replaces it unseen
function Suggestion({ memory, replaced }: { memory: MemoryJson, replaced: ReadonlyMap<string, string> }) {
  const old = memory.replaces && changeOf(memory) === 'update'
    ? replaced.get(memory.replaces)
    : undefined
  return (
    <>
      <MemoryText text={memory.text} />
      {old !== undefined && <span className="block max-w-[60ch] break-words whitespace-pre-wrap text-muted-foreground">{`Replaces: ${old}`}</span>}
    </>
  )
}

function columns(names: ReadonlyMap<string, string>, timeZone: string | null, replaced: ReadonlyMap<string, string>): Column<MemoryJson>[] {
  return [
    {
      id: 'text',
      header: 'Suggestion',
      cell: m => <Suggestion memory={m} replaced={replaced} />,
      className: 'whitespace-normal py-2',
    },
    {
      id: 'change',
      header: 'Change',
      sortValue: m => CHANGE_LABELS[changeOf(m)],
      cell: (m) => {
        const change = changeOf(m)
        return <StatusBadge tone={CHANGE_TONE[change]} label={CHANGE_LABELS[change]} />
      },
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
      id: 'created',
      header: 'Suggested',
      sortValue: m => Date.parse(m.createdAt),
      firstSort: 'desc',
      cell: m => <Stamp at={m.createdAt} timeZone={timeZone} />,
      className: 'max-lg:hidden',
    },
  ]
}

// what learning proposes while approval is on (memoryApproval): each approved, edited first or rejected here
export function SuggestionInbox({ devices, names, heading }: {
  devices: readonly DeviceView[]
  names: ReadonlyMap<string, string>
  heading: RefObject<HTMLElement | null>
}) {
  const api = useApi()
  const timeZone = useTimeZone()
  const list = useQuery(suggestionsQueryOptions(api))
  const [refusal, setRefusal] = useState<string>()
  const replaced = useReplacedTexts(list.data)
  const cols = useMemo(() => columns(names, timeZone, replaced), [names, timeZone, replaced])
  return (
    <div className="flex flex-col gap-3">
      <DataTable
        caption="Suggestions"
        columns={cols}
        rows={list.data}
        rowKey={m => m.id}
        initialSort={NEWEST}
        status={list.isError
          ? 'error'
          : list.isPending ? 'loading' : 'ready'}
        error={<ErrorState title="The suggestions could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
        empty={(
          <EmptyState
            icon={InboxIcon}
            title="No suggestions"
            description="With memory approval on, what the assistant learns waits here until you approve or reject it."
          />
        )}
        rowActions={m => <SuggestionActions memory={m} devices={devices} heading={heading} onRefused={setRefusal} />}
      />
      {refusal && <p role="alert" className="text-sm text-destructive">{refusal}</p>}
      {list.data?.length === SUGGESTIONS_LIMIT && (
        <p className="text-sm text-muted-foreground">{`The first ${SUGGESTIONS_LIMIT} are shown; the rest follow as these are reviewed.`}</p>
      )}
    </div>
  )
}
