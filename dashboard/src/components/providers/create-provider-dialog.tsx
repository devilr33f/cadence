import type { RefObject } from 'react'
import type { CreateProviderRequest, ModuleInfo } from '@/api/gen'
import { useState } from 'react'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { CreateProviderForm } from './create-provider-form'

// the form, with any secret typed into it, goes the moment the dialog starts closing, not after its fade, so a reopen in
// that window starts empty; it cannot be dismissed while the create is on its way
export function CreateProviderDialog({ open, onOpenChange, finalFocus, modules, onCreate }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  // the page's own action: the empty state's button that may have opened it goes once the first instance lands
  finalFocus: RefObject<HTMLElement | null>
  // the server's catalog
  modules: readonly ModuleInfo[]
  onCreate: (request: CreateProviderRequest) => Promise<void>
}) {
  const [pending, setPending] = useState(false)
  const [opening, setOpening] = useState(0)
  const change = (next: boolean) => {
    if (!next)
      setOpening(n => n + 1)
    onOpenChange(next)
  }
  return (
    <Dialog open={open} onOpenChange={next => !pending && change(next)}>
      <DialogContent finalFocus={finalFocus} className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Add a provider</DialogTitle>
          <DialogDescription>An instance of a module, with its configuration and its secrets; settings name it to use it.</DialogDescription>
        </DialogHeader>
        <CreateProviderForm key={opening} modules={modules} onCreate={onCreate} onDone={() => change(false)} onPendingChange={setPending} />
      </DialogContent>
    </Dialog>
  )
}
