import type { TaskJson } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import type { Topic } from '@/lib/live/topics'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ListChecksIcon } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { MoreButton } from '@/components/data/more-button'
import { PageHeader } from '@/components/data/page-header'
import { Stamp } from '@/components/data/stamp'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { useDeviceNames } from '@/hooks/use-device-names'
import { useLiveTopics } from '@/lib/live/use-live'
import { canAskMore } from '@/lib/paging'
import { TASKS_MAX, TASKS_PAGE, tasksQueryOptions } from '@/lib/tasks'
import { useTimeZone } from '@/lib/time-zone'
import { TaskStatus } from './task-status'
import { STATUS_FILTERS, statusLabel } from './task-words'

const TOPICS: readonly Topic[] = ['tasks']

const NEWEST = { column: 'created', direction: 'desc' } as const

function columns(names: ReadonlyMap<string, string>, timeZone: string | null): Column<TaskJson>[] {
  return [
    {
      id: 'title',
      header: 'Task',
      sortValue: t => t.title,
      cell: t => (
        <div className="flex max-w-[48ch] min-w-48 flex-col py-1.5 whitespace-normal">
          <Link to="/tasks/$id" params={{ id: t.shortId }} className="font-medium break-words underline-offset-3 hover:underline">{t.title}</Link>
          <span className="font-mono text-xs text-muted-foreground">{t.shortId}</span>
        </div>
      ),
    },
    {
      id: 'kind',
      header: 'Kind',
      sortValue: t => t.kind,
      cell: t => <span className="font-mono text-xs">{t.kind}</span>,
      className: 'max-md:hidden',
    },
    {
      id: 'status',
      header: 'Status',
      sortValue: t => statusLabel(t.status),
      cell: t => <TaskStatus task={t} />,
    },
    {
      id: 'device',
      header: 'Speaker',
      sortValue: t => t.deviceId === null
        ? null
        : (names.get(t.deviceId) ?? t.deviceId),
      cell: t => t.deviceId === null
        ? <span className="text-muted-foreground">—</span>
        : names.get(t.deviceId) ?? <span className="font-mono text-xs">{t.deviceId}</span>,
      className: 'max-lg:hidden',
    },
    {
      id: 'created',
      header: 'Started',
      sortValue: t => Date.parse(t.createdAt),
      firstSort: 'desc',
      cell: t => <Stamp at={t.createdAt} timeZone={timeZone} />,
    },
    {
      id: 'updated',
      header: 'Updated',
      sortValue: t => Date.parse(t.updatedAt),
      firstSort: 'desc',
      cell: t => <Stamp at={t.updatedAt} timeZone={timeZone} />,
      className: 'max-lg:hidden',
    },
  ]
}

export function TasksPage() {
  const api = useApi()
  useLiveTopics(TOPICS)
  const timeZone = useTimeZone()
  const names = useDeviceNames()
  const [status, setStatus] = useState('')
  const [limit, setLimit] = useState(TASKS_PAGE)
  const list = useQuery({
    ...tasksQueryOptions(api, status, limit),
    placeholderData: keepPreviousData,
  })
  const cols = useMemo(() => columns(names, timeZone), [names, timeZone])
  const filterId = useId()

  return (
    <>
      <PageHeader
        title="Tasks"
        description="Background jobs the assistant handed to Hermes: what each is doing, what it asks you, and how it ended."
      />
      <div className="flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={filterId}>Status</Label>
          <NativeSelect
            id={filterId}
            size="sm"
            value={status}
            onChange={(e) => {
              setStatus(e.target.value)
              setLimit(TASKS_PAGE)
            }}
          >
            {STATUS_FILTERS.map(f => <NativeSelectOption key={f.value} value={f.value}>{f.label}</NativeSelectOption>)}
          </NativeSelect>
        </div>
        <DataTable
          caption="Tasks"
          columns={cols}
          rows={list.data}
          rowKey={t => t.shortId}
          initialSort={NEWEST}
          status={list.isError
            ? 'error'
            : list.isPending ? 'loading' : 'ready'}
          error={<ErrorState title="The tasks could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
          empty={status === ''
            ? <EmptyState icon={ListChecksIcon} title="No tasks yet" description="A task starts when the assistant hands a longer job to Hermes; it shows here with what it asks you." />
            : <EmptyState icon={ListChecksIcon} title="No tasks in this state" description="Choose another status, or All." />}
        />
        {canAskMore(list, limit, TASKS_MAX) && (
          <MoreButton busy={list.isFetching} onMore={() => setLimit(n => Math.min(n + TASKS_PAGE, TASKS_MAX))}>Show more tasks</MoreButton>
        )}
      </div>
    </>
  )
}
