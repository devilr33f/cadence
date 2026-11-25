import type { ComponentProps, ReactElement } from 'react'
import { useState } from 'react'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { MemoryForm } from './memory-form'

type FormProps = Omit<ComponentProps<typeof MemoryForm>, 'onDone' | 'onPendingChange'>

// a memory's form in a dialog: it goes the moment the dialog starts closing (keyed by the opening), and the dialog
// cannot be dismissed while its request runs
export function MemoryDialog({ open, onOpenChange, trigger, title, description, finalFocus, ...form }: FormProps & {
  open: boolean
  onOpenChange: (open: boolean) => void
  // a row's own button; without one the page opens it
  trigger?: ReactElement
  title: string
  description: string
  finalFocus?: ComponentProps<typeof DialogContent>['finalFocus']
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
      {trigger && <DialogTrigger render={trigger} />}
      <DialogContent finalFocus={finalFocus}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <MemoryForm key={opening} {...form} onDone={() => change(false)} onPendingChange={setPending} />
      </DialogContent>
    </Dialog>
  )
}
