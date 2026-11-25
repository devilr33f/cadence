import type { ProcView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { useQuery } from '@tanstack/react-query'
import { useId } from 'react'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { ErrorState } from '@/components/data/states'
import { deviceProcessesQueryOptions } from '@/lib/metrics'
import { bytesText, percentText } from '@/lib/units'

// pid, comm, cpu and rss are all the server has of a process: never its command line
const COLUMNS: Column<ProcView>[] = [
  {
    id: 'comm',
    header: 'Process',
    cell: p => <span className="font-mono text-xs">{p.comm}</span>,
  },
  {
    id: 'pid',
    header: 'PID',
    align: 'end',
    cell: p => p.pid,
  },
  {
    id: 'cpu',
    header: 'CPU',
    align: 'end',
    // the device counts it in percent of one core
    cell: p => percentText(p.cpu / 100),
  },
  {
    id: 'rss',
    header: 'Memory',
    align: 'end',
    cell: p => bytesText(p.rss),
  },
]

function ProcessTable({ title, rows, status, error, onRetry }: {
  title: string
  rows?: readonly ProcView[]
  status: 'ready' | 'loading' | 'error'
  error: unknown
  onRetry: () => void
}) {
  const titleId = useId()
  return (
    <section aria-labelledby={titleId} className="flex min-w-0 flex-col gap-2">
      <h3 id={titleId} className="text-base font-semibold">{title}</h3>
      <DataTable
        caption={title}
        columns={COLUMNS}
        rows={rows}
        rowKey={p => String(p.pid)}
        status={status}
        error={<ErrorState title="The processes could not be read" error={error} onRetry={onRetry} />}
        empty={<p className="px-3 py-6 text-center text-sm text-muted-foreground">No processes reported yet.</p>}
      />
    </section>
  )
}

// the device's latest top ten by cpu and by memory, as it reported them with its last round
export function Processes({ deviceId }: { deviceId: string }) {
  const api = useApi()
  const processes = useQuery(deviceProcessesQueryOptions(api, deviceId))
  const status = processes.isError
    ? 'error'
    : processes.isPending ? 'loading' : 'ready'
  const retry = () => void processes.refetch()
  return (
    <div className="grid gap-6 lg:grid-cols-2">
      <ProcessTable title="Top by CPU" rows={processes.data?.topCpu ?? []} status={status} error={processes.error} onRetry={retry} />
      <ProcessTable title="Top by memory" rows={processes.data?.topRss ?? []} status={status} error={processes.error} onRetry={retry} />
    </div>
  )
}
