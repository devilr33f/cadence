import type { RefObject } from 'react'
import type { DeviceView, GroupRequest, GroupView } from '@/api/gen'
import { useState } from 'react'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { GroupForm } from './group-form'

// what the dialog is open on: a new group, or one to edit
export type GroupTarget = { kind: 'create' } | { kind: 'edit', group: GroupView }

// the form goes the moment the dialog starts closing (keyed by the opening), so a reopen starts from what it opens on;
// it cannot be dismissed while the save is on its way
export function GroupDialog({ target, onClose, finalFocus, devices, groups, onSave }: {
  target?: GroupTarget
  onClose: () => void
  finalFocus: RefObject<HTMLElement | null>
  devices: readonly DeviceView[]
  // every group, for the names a save may not reuse
  groups: readonly GroupView[]
  onSave: (id: string | undefined, body: GroupRequest) => Promise<unknown>
}) {
  const [pending, setPending] = useState(false)
  const [opening, setOpening] = useState(0)
  const [shown, setShown] = useState(target)
  if (target && target !== shown)
    setShown(target)
  const close = () => {
    setOpening(n => n + 1)
    onClose()
  }
  const group = shown?.kind === 'edit'
    ? shown.group
    : undefined
  const taken = groups.filter(g => g.id !== group?.id).map(g => g.name)
  return (
    <Dialog open={target !== undefined} onOpenChange={next => !next && !pending && close()}>
      <DialogContent finalFocus={finalFocus} className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {group
              ? `Edit ${group.name}`
              : 'Add a group'}
          </DialogTitle>
          <DialogDescription>
            Devices that play the same music in step, or two speakers as one stereo pair. A device is in one group at
            most.
          </DialogDescription>
        </DialogHeader>
        <GroupForm
          key={`${opening}-${group?.id ?? ''}`}
          group={group}
          devices={devices}
          taken={taken}
          onSave={onSave}
          onDone={close}
          onPendingChange={setPending}
        />
      </DialogContent>
    </Dialog>
  )
}
