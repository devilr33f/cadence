import type { GroupTarget } from './group-dialog'
import type { DeviceView } from '@/api/gen'
import type { Topic } from '@/lib/live/topics'
import { useQuery } from '@tanstack/react-query'
import { GroupIcon, PlusIcon } from 'lucide-react'
import { useMemo, useRef, useState } from 'react'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { devicesQueryOptions, isEnrolled } from '@/lib/devices'
import { groupsQueryOptions, useGroupActions } from '@/lib/groups'
import { useLiveTopics } from '@/lib/live/use-live'
import { GroupDialog } from './group-dialog'
import { groupColumns } from './group-table'

const TOPICS: readonly Topic[] = ['groups', 'devices']
const BY_NAME = { column: 'name', direction: 'asc' } as const
const NO_DEVICES: DeviceView[] = []

export function GroupsPage() {
  useLiveTopics(TOPICS)
  const api = useApi()
  const list = useQuery(groupsQueryOptions(api))
  const devices = useQuery(devicesQueryOptions(api))
  const actions = useGroupActions()
  const [target, setTarget] = useState<GroupTarget>()
  const addRef = useRef<HTMLButtonElement>(null)

  const enrolled = useMemo(() => (devices.data ?? NO_DEVICES).filter(isEnrolled), [devices.data])
  const names = useMemo(() => new Map(enrolled.map(d => [d.id, d.name])), [enrolled])
  const columns = useMemo(() => groupColumns(names), [names])
  // a create checks its name and its devices against the lists: until both are read it waits
  const ready = list.data !== undefined && devices.data !== undefined

  const add = (size: 'default' | 'lg') => (
    <Button
      ref={size === 'lg'
        ? addRef
        : undefined}
      size={size}
      aria-disabled={!ready || undefined}
      onClick={() => {
        if (ready)
          setTarget({ kind: 'create' })
      }}
    >
      <PlusIcon aria-hidden="true" />
      Add a group
    </Button>
  )

  return (
    <>
      <PageHeader
        title="Groups"
        description="Devices that play the same music in step, and stereo pairs: two speakers as one."
        actions={add('lg')}
      />
      <DataTable
        caption="Groups"
        columns={columns}
        rows={list.data}
        rowKey={g => g.id}
        initialSort={BY_NAME}
        status={list.isError
          ? 'error'
          : list.isPending ? 'loading' : 'ready'}
        error={<ErrorState title="The groups could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
        empty={(
          <EmptyState
            icon={GroupIcon}
            title="No groups"
            description="A group plays one stream on several devices at once; a stereo pair splits it into left and right."
            action={add('default')}
          />
        )}
      />
      <GroupDialog
        target={target}
        onClose={() => setTarget(undefined)}
        finalFocus={addRef}
        devices={enrolled}
        groups={list.data ?? []}
        onSave={actions.save}
      />
    </>
  )
}
