import type { DeviceRow } from './device-table'
import type { Topic } from '@/lib/live/topics'
import { useQuery } from '@tanstack/react-query'
import { SpeakerIcon } from 'lucide-react'
import { useMemo } from 'react'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState } from '@/components/data/states'
import { devicesQueryOptions } from '@/lib/devices'
import { useLiveTopics } from '@/lib/live/use-live'
import { useTimeZone } from '@/lib/time-zone'
import { ClaimsNotice } from './claims-notice'
import { deviceColumns } from './device-table'
import { EnrollLink } from './enroll-link'
import { useBoardNames } from './use-boards'

const TOPICS: readonly Topic[] = ['devices']

export function DevicesPage() {
  useLiveTopics(TOPICS)
  const api = useApi()
  const timeZone = useTimeZone()
  const boards = useBoardNames()
  // read again on the devices topic's events, a feed gap and focus
  const list = useQuery(devicesQueryOptions(api))
  const columns = useMemo(() => deviceColumns(timeZone), [timeZone])

  const rows: DeviceRow[] | undefined = list.data?.map(device => ({
    device,
    board: boards.get(device.model) ?? device.model,
  }))

  return (
    <>
      <PageHeader
        title="Devices"
        description="Every speaker in the household: whether it is linked, its board, and when it last connected."
        actions={<EnrollLink size="lg" />}
      />
      <ClaimsNotice />
      <DataTable
        caption="Devices"
        columns={columns}
        rows={rows}
        rowKey={r => r.device.id}
        status={list.isError
          ? 'error'
          : list.isPending ? 'loading' : 'ready'}
        error={<ErrorState title="The devices could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
        empty={(
          <EmptyState
            icon={SpeakerIcon}
            title="No speakers yet"
            description="Enroll one with a code: the speaker runs a one-line command and shows up here."
            action={<EnrollLink />}
          />
        )}
      />
    </>
  )
}
