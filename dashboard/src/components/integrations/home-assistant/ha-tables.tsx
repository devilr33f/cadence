import type { AreaJson, DeviceView, UsageJson } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { useQuery } from '@tanstack/react-query'
import { ActivityIcon, MapIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { usageQueryOptions } from '@/lib/home-assistant'

const AREA_COLUMNS: Column<AreaJson>[] = [
  {
    id: 'name',
    header: 'Name',
    sortValue: a => a.name,
    cell: a => <span className="font-medium">{a.name}</span>,
  },
  {
    id: 'id',
    header: 'Area',
    sortValue: a => a.id,
    cell: a => <span className="font-mono text-xs">{a.id}</span>,
  },
  {
    id: 'aliases',
    header: 'Aliases',
    cell: a => a.aliases?.length
      ? <span className="whitespace-normal">{a.aliases.join(', ')}</span>
      : <span className="text-muted-foreground">—</span>,
  },
]

export function HomeAssistantAreas({ areas, status, error, onRetry }: {
  areas: readonly AreaJson[] | undefined
  status: 'ready' | 'loading' | 'error'
  error: unknown
  onRetry: () => void
}) {
  return (
    <DataTable
      caption="Areas"
      columns={AREA_COLUMNS}
      rows={areas}
      rowKey={a => a.id}
      status={status}
      error={<ErrorState title="The areas could not be listed" error={error} onRetry={onRetry} />}
      empty={<EmptyState icon={MapIcon} title="No areas" description="Areas set in Home Assistant narrow a name several rooms share." />}
    />
  )
}

const USAGE_COLUMNS: Column<UsageJson>[] = [
  {
    id: 'entity',
    header: 'Entity',
    sortValue: u => u.entityId,
    cell: u => <span className="font-mono text-xs">{u.entityId}</span>,
  },
  {
    id: 'score',
    header: 'Score',
    align: 'end',
    sortValue: u => u.score,
    firstSort: 'desc',
    cell: u => Number(u.score.toFixed(2)),
  },
]

// the entities used most, the household's or one speaker's, by the server's score
export function HomeAssistantUsage({ devices }: { devices: readonly DeviceView[] }) {
  const api = useApi()
  const selectId = useId()
  const [deviceId, setDeviceId] = useState('')
  const usage = useQuery(usageQueryOptions(api, deviceId))
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={selectId}>Used by</Label>
        <NativeSelect id={selectId} size="sm" value={deviceId} onChange={e => setDeviceId(e.target.value)}>
          <NativeSelectOption value="">The household</NativeSelectOption>
          {devices.map(device => <NativeSelectOption key={device.id} value={device.id}>{device.name}</NativeSelectOption>)}
        </NativeSelect>
      </div>
      <DataTable
        caption="Most used entities"
        columns={USAGE_COLUMNS}
        rows={usage.data}
        rowKey={u => u.entityId}
        status={usage.isError
          ? 'error'
          : usage.isPending ? 'loading' : 'ready'}
        error={<ErrorState title="The usage could not be read" error={usage.error} onRetry={() => void usage.refetch()} />}
        empty={<EmptyState icon={ActivityIcon} title="Nothing used yet" description="An entity the assistant controls counts here, so its name reaches the model first." />}
      />
    </div>
  )
}
