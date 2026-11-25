import type { RefObject } from 'react'
import type { Instance, ModuleInfo, UpdateProviderRequest } from '@/api/gen'
import { useState } from 'react'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { CreateProviderForm } from './create-provider-form'

// the same form as create, with the stored config and write-only secrets; closing unmounts it immediately, so no value
// typed or loaded survives the dialog, even while its fade runs
export function EditProviderDialog({ instance, onOpenChange, finalFocus, modules, onUpdate }: {
  instance: Instance
  onOpenChange: (open: boolean) => void
  finalFocus: RefObject<HTMLElement | null>
  modules: readonly ModuleInfo[]
  onUpdate: (name: string, request: UpdateProviderRequest) => Promise<void>
}) {
  const [pending, setPending] = useState(false)
  return (
    <Dialog open onOpenChange={next => !pending && onOpenChange(next)}>
      <DialogContent finalFocus={finalFocus} className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{`Edit ${instance.name}`}</DialogTitle>
          <DialogDescription>Change its configuration or secrets in place. Empty secret fields keep their stored values.</DialogDescription>
        </DialogHeader>
        <CreateProviderForm
          instance={instance}
          modules={modules}
          onUpdate={onUpdate}
          onDone={() => onOpenChange(false)}
          onPendingChange={setPending}
        />
      </DialogContent>
    </Dialog>
  )
}
