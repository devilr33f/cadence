import type { CompactionJson, DeviceView, RunJson } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { HistoryIcon } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { MoreButton } from '@/components/data/more-button'
import { Stamp } from '@/components/data/stamp'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { compactionsQueryOptions, learningQueryOptions, LOG_MAX, LOG_PAGE } from '@/lib/memory'
import { canAskMore } from '@/lib/paging'
import { useTimeZone } from '@/lib/time-zone'

const NEWEST = { column: 'created', direction: 'desc' } as const

function runColumns(timeZone: string | null): Column<RunJson>[] {
  return [
    {
      id: 'created',
      header: 'When',
      sortValue: r => Date.parse(r.createdAt),
      firstSort: 'desc',
      cell: r => <Stamp at={r.createdAt} timeZone={timeZone} />,
    },
    {
      id: 'applied',
      header: 'Applied',
      align: 'end',
      sortValue: r => r.applied,
      cell: r => r.applied,
    },
    {
      id: 'pending',
      header: 'For review',
      align: 'end',
      sortValue: r => r.pending,
      cell: r => r.pending,
    },
    {
      id: 'dropped',
      header: 'Dropped',
      align: 'end',
      sortValue: r => r.dropped,
      cell: r => r.dropped,
    },
    {
      id: 'memories',
      header: 'Memories',
      align: 'end',
      cell: r => r.memoryIds?.length ?? 0,
    },
  ]
}

function compactionColumns(timeZone: string | null): Column<CompactionJson>[] {
  return [
    {
      id: 'created',
      header: 'When',
      sortValue: c => Date.parse(c.createdAt),
      firstSort: 'desc',
      cell: c => <Stamp at={c.createdAt} timeZone={timeZone} />,
    },
    {
      id: 'model',
      header: 'Model',
      cell: c => <span className="font-mono text-xs">{c.model}</span>,
      className: 'max-md:hidden',
    },
    {
      id: 'summary',
      header: 'Summary',
      cell: c => <span className="block max-w-[70ch] min-w-56 break-words whitespace-pre-wrap">{c.summary}</span>,
      className: 'whitespace-normal py-2',
    },
    {
      id: 'threads',
      header: 'Open threads',
      cell: c => (c.openThreads?.length
        ? (
            <ul className="flex max-w-[40ch] min-w-40 flex-col gap-1 whitespace-normal">
              {c.openThreads.map((thread, i) => (
                // eslint-disable-next-line react/no-array-index-key -- a summary may name one thread twice
                <li key={`${i}-${thread}`} className="break-words">{thread}</li>
              ))}
            </ul>
          )
        : <span className="text-muted-foreground">None</span>),
      className: 'max-lg:hidden',
    },
  ]
}

function DeviceLog({ deviceId }: { deviceId: string }) {
  const api = useApi()
  const timeZone = useTimeZone()
  const [runsLimit, setRunsLimit] = useState(LOG_PAGE)
  const [compactionsLimit, setCompactionsLimit] = useState(LOG_PAGE)
  // a page asked for more keeps the rows it has until the longer one lands
  const runs = useQuery({
    ...learningQueryOptions(api, deviceId, runsLimit),
    placeholderData: keepPreviousData,
  })
  const compactions = useQuery({
    ...compactionsQueryOptions(api, deviceId, compactionsLimit),
    placeholderData: keepPreviousData,
  })
  const runCols = useMemo(() => runColumns(timeZone), [timeZone])
  const compactionCols = useMemo(() => compactionColumns(timeZone), [timeZone])
  const more = (limit: number) => Math.min(limit + LOG_PAGE, LOG_MAX)

  return (
    <>
      <div className="flex flex-col gap-3">
        <h3 className="text-base font-semibold">Learning runs</h3>
        <p className="-mt-2 text-sm text-muted-foreground">What each compaction taught: memories applied, held for review, or dropped by the filters.</p>
        <DataTable
          caption="Learning runs"
          columns={runCols}
          rows={runs.data}
          rowKey={r => r.id}
          initialSort={NEWEST}
          status={runs.isError
            ? 'error'
            : runs.isPending ? 'loading' : 'ready'}
          error={<ErrorState title="The learning runs could not be listed" error={runs.error} onRetry={() => void runs.refetch()} />}
          empty={<EmptyState icon={HistoryIcon} title="Nothing learned yet" description="A run follows each compaction of this speaker's conversation." />}
        />
        {canAskMore(runs, runsLimit, LOG_MAX) && (
          <MoreButton busy={runs.isFetching} onMore={() => setRunsLimit(more)}>Show more runs</MoreButton>
        )}
      </div>
      <div className="flex flex-col gap-3">
        <h3 className="text-base font-semibold">Compactions</h3>
        <p className="-mt-2 text-sm text-muted-foreground">The summaries an idle conversation is folded into; the turns after the newest one stay word for word.</p>
        <DataTable
          caption="Compactions"
          columns={compactionCols}
          rows={compactions.data}
          rowKey={c => c.id}
          initialSort={NEWEST}
          status={compactions.isError
            ? 'error'
            : compactions.isPending ? 'loading' : 'ready'}
          error={<ErrorState title="The compactions could not be listed" error={compactions.error} onRetry={() => void compactions.refetch()} />}
          empty={<EmptyState icon={HistoryIcon} title="No compactions yet" description="A conversation idle for compactAfter minutes is summarized here." />}
        />
        {canAskMore(compactions, compactionsLimit, LOG_MAX) && (
          <MoreButton busy={compactions.isFetching} onMore={() => setCompactionsLimit(more)}>Show more compactions</MoreButton>
        )}
      </div>
    </>
  )
}

// a speaker's learning log and compactions, newest first; read-only
export function LearningLog({ devices }: { devices: readonly DeviceView[] }) {
  const [chosen, setChosen] = useState<string>()
  const selectId = useId()
  const deviceId = chosen ?? devices[0]?.id
  if (!deviceId) {
    return (
      <div className="rounded-lg border bg-card">
        <EmptyState icon={HistoryIcon} title="No speakers yet" description="Each speaker learns from its own conversations; enroll one to see what it learned." />
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={selectId}>Speaker</Label>
        <NativeSelect id={selectId} size="sm" value={deviceId} onChange={e => setChosen(e.target.value)}>
          {devices.map(device => <NativeSelectOption key={device.id} value={device.id}>{device.name}</NativeSelectOption>)}
        </NativeSelect>
      </div>
      {/* keyed by the speaker: each starts from its first page */}
      <DeviceLog key={deviceId} deviceId={deviceId} />
    </div>
  )
}
