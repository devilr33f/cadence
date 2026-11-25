import type { BusySignal, DeviceView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { useQuery } from '@tanstack/react-query'
import { describeError } from '@/api/describe-error'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { Stamp } from '@/components/data/stamp'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { toast } from '@/components/ui/toast-manager'
import { BUSY_REFRESH_MS, busyQueryOptions, isHomeAssistantSource, REASON_TEXT, sourceText, useBusyActions } from '@/lib/busy'
import { useTimeZone } from '@/lib/time-zone'
import { SettingLink } from '../setting-link'
import { DndControl } from './dnd-control'

function speakersText(signal: BusySignal, devices: readonly DeviceView[]): string {
  if (signal.devices === null)
    return 'Every speaker'
  return signal.devices.map(id => devices.find(device => device.id === id)?.name ?? 'A removed speaker').join(', ')
}

// the signals in force, by source, and the owner's do-not-disturb; home assistant's are its states' own and go with them
export function BusySection({ devices }: { devices: readonly DeviceView[] }) {
  const api = useApi()
  const timeZone = useTimeZone()
  const actions = useBusyActions()
  const busy = useQuery({
    ...busyQueryOptions(api),
    refetchInterval: BUSY_REFRESH_MS,
  })

  async function clear(source: string) {
    try {
      await actions.clear(source)
    }
    catch (error) {
      toast.add({
        type: 'error',
        title: 'The signal was not cleared',
        description: describeError(error),
      })
    }
  }

  const columns: Column<BusySignal>[] = [
    {
      id: 'source',
      header: 'Source',
      cell: signal => (
        <span className="flex flex-col">
          <span>{sourceText(signal.source)}</span>
          <code className="font-mono text-xs break-all text-muted-foreground">{signal.source}</code>
        </span>
      ),
      sortValue: signal => signal.source,
    },
    {
      id: 'reason',
      header: 'Reason',
      cell: signal => REASON_TEXT[signal.reason],
    },
    {
      id: 'speakers',
      header: 'Speakers',
      cell: signal => speakersText(signal, devices),
      className: 'max-md:hidden',
    },
    {
      id: 'expires',
      header: 'Ends',
      cell: signal => signal.expiresAt
        ? <Stamp at={signal.expiresAt} timeZone={timeZone} />
        : <span className="text-muted-foreground">While the state holds</span>,
      sortValue: signal => signal.expiresAt ?? undefined,
    },
  ]

  return (
    <div className="flex flex-col gap-4">
      <DndControl signals={busy.data?.signals} />
      <DataTable
        caption="Busy signals"
        columns={columns}
        rows={busy.data?.signals}
        rowKey={signal => signal.source}
        status={busy.isPending
          ? 'loading'
          : busy.isError
            ? 'error'
            : 'ready'}
        error={<ErrorState title="The busy signals could not be read" error={busy.error} onRetry={() => void busy.refetch()} />}
        empty={<EmptyState title="No one is busy" description="A call, a meeting, a screenshare or a do-not-disturb from the dashboard, a voice request, Home Assistant or the API shows here while it holds." />}
        rowActions={signal => isHomeAssistantSource(signal.source)
          ? null
          : (
              <Button type="button" variant="ghost" size="sm" onClick={() => void clear(signal.source)}>
                Clear
                <span className="sr-only">{`: ${signal.source}`}</span>
              </Button>
            )}
      />
      <p className="flex flex-wrap items-center gap-x-2 text-sm text-muted-foreground">
        Home Assistant states that make every speaker busy are a setting.
        <SettingLink settingKey="busyEntities" />
      </p>
    </div>
  )
}
