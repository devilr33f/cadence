import type { DeviceView, Subscription } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { useQuery } from '@tanstack/react-query'
import { PlusIcon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { useApi } from '@/api/use-api'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { DataTable } from '@/components/data/data-table'
import { Stamp } from '@/components/data/stamp'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Tag } from '@/components/data/tag'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { toast } from '@/components/ui/toast-manager'
import { providersQueryOptions } from '@/lib/providers'
import { CREATED_BY_TEXT, DELIVERY_TEXT, EVENT_TEXT, subscriptionsQueryOptions, UNTIL_TEXT, useSubscriptionActions } from '@/lib/subscriptions'
import { useTimeZone } from '@/lib/time-zone'
import { SubscriptionForm } from './subscription-form'

const ALL = ''

function nameOf(devices: readonly DeviceView[], id: string): string {
  return devices.find(device => device.id === id)?.name ?? 'A removed speaker'
}

function Events({ events }: { events: readonly string[] }) {
  if (events.length === 0)
    return <span className="text-muted-foreground">Every event</span>
  return (
    <span className="flex flex-wrap gap-1">
      {events.map(name => <Tag key={name}>{EVENT_TEXT[name as keyof typeof EVENT_TEXT] ?? name}</Tag>)}
    </span>
  )
}

// what each speaker follows in other systems: add, list per speaker, remove
export function SubscriptionsSection({ devices }: { devices: readonly DeviceView[] }) {
  const api = useApi()
  const timeZone = useTimeZone()
  const actions = useSubscriptionActions()
  const filterId = useId()
  const addRef = useRef<HTMLButtonElement>(null)
  const [device, setDevice] = useState(ALL)
  const [open, setOpen] = useState(false)
  const [pending, setPending] = useState(false)
  const [opening, setOpening] = useState(0)
  const list = useQuery(subscriptionsQueryOptions(api))
  const instances = useQuery(providersQueryOptions(api)).data ?? []
  const rows = list.data?.subscriptions.filter(s => device === ALL || s.device === device)

  async function remove(subscription: Subscription) {
    try {
      await actions.remove(subscription.id)
    }
    catch (error) {
      toast.add({
        type: 'error',
        title: 'The subscription was not removed',
        description: describeError(error),
      })
      throw error
    }
  }

  const columns: Column<Subscription>[] = [
    {
      id: 'speaker',
      header: 'Speaker',
      cell: s => nameOf(devices, s.device),
      sortValue: s => nameOf(devices, s.device),
    },
    {
      id: 'topic',
      header: 'Topic',
      cell: s => <code className="font-mono text-xs break-all">{s.topic}</code>,
      sortValue: s => s.topic,
    },
    {
      id: 'events',
      header: 'Events',
      cell: s => <Events events={s.events} />,
      className: 'max-md:hidden',
    },
    {
      id: 'delivery',
      header: 'Delivery',
      cell: s => DELIVERY_TEXT[s.delivery],
    },
    {
      id: 'until',
      header: 'Lasts',
      cell: s => UNTIL_TEXT[s.until],
      className: 'max-lg:hidden',
    },
    {
      id: 'by',
      header: 'Made by',
      cell: s => CREATED_BY_TEXT[s.createdBy],
      className: 'max-lg:hidden',
    },
    {
      id: 'created',
      header: 'Made',
      cell: s => <Stamp at={s.createdAt} timeZone={timeZone} />,
      sortValue: s => s.createdAt,
      firstSort: 'desc',
      className: 'max-xl:hidden',
    },
  ]

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex flex-col gap-2">
          <Label htmlFor={filterId}>Speaker</Label>
          <NativeSelect id={filterId} size="sm" value={device} onChange={e => setDevice(e.target.value)}>
            <NativeSelectOption value={ALL}>Every speaker</NativeSelectOption>
            {devices.map(d => <NativeSelectOption key={d.id} value={d.id}>{d.name}</NativeSelectOption>)}
          </NativeSelect>
        </div>
        <Button ref={addRef} type="button" variant="outline" onClick={() => setOpen(true)}>
          <PlusIcon aria-hidden="true" />
          Follow a topic
        </Button>
      </div>
      <DataTable
        caption="Subscriptions"
        columns={columns}
        rows={rows}
        rowKey={s => s.id}
        status={list.isPending
          ? 'loading'
          : list.isError
            ? 'error'
            : 'ready'}
        error={<ErrorState title="The subscriptions could not be read" error={list.error} onRetry={() => void list.refetch()} />}
        empty={(
          <EmptyState
            title="Nothing is followed"
            description="A subscription has a speaker tell when a thread stops, asks or fails. Make one here, or ask a speaker to follow it."
          />
        )}
        initialSort={{ column: 'created', direction: 'desc' }}
        rowActions={s => (
          <ConfirmDialog
            trigger={(
              <Button type="button" variant="ghost" size="sm">
                Remove
                <span className="sr-only">{`: ${s.topic} on ${nameOf(devices, s.device)}`}</span>
              </Button>
            )}
            title="Stop following this topic?"
            description={(
              <>
                {`${nameOf(devices, s.device)} no longer tells of `}
                <code className="font-mono text-xs break-all">{s.topic}</code>
                .
              </>
            )}
            confirmLabel="Remove the subscription"
            tone="destructive"
            finalFocus={addRef}
            onConfirm={() => remove(s)}
          />
        )}
      />
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (pending)
            return
          if (!next)
            setOpening(n => n + 1)
          setOpen(next)
        }}
      >
        <DialogContent finalFocus={addRef} className="sm:max-w-xl">
          <SubscriptionForm
            key={opening}
            devices={devices}
            instances={instances}
            initialDevice={device === ALL
              ? undefined
              : device}
            onCreate={actions.create}
            onCreated={() => {
              setOpening(n => n + 1)
              setOpen(false)
              toast.add({ type: 'success', title: 'The topic is followed' })
            }}
            onPendingChange={setPending}
          />
        </DialogContent>
      </Dialog>
    </div>
  )
}
