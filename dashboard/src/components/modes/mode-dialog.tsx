import type { RefObject } from 'react'
import type { ModeView, PutModeRequest } from '@/api/gen'
import { useState } from 'react'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ModeForm } from './mode-form'

// what the dialog is open on: a new mode, or one to edit
export type ModeTarget = { kind: 'create' } | { kind: 'edit', mode: ModeView }

// the form goes the moment the dialog starts closing (keyed by the opening), so a reopen starts from what it opens on;
// it cannot be dismissed while the put is on its way
export function ModeDialog({ target, onClose, finalFocus, taken, onSave }: {
  target?: ModeTarget
  onClose: () => void
  // the button that opened it, or the page's action for a create (the empty state's button goes with the first mode)
  finalFocus: RefObject<HTMLElement | null>
  taken: readonly string[]
  onSave: (name: string, body: PutModeRequest) => Promise<void>
}) {
  const [pending, setPending] = useState(false)
  const [opening, setOpening] = useState(0)
  const [shown, setShown] = useState(target)
  // what it was open on stays while it fades out
  if (target && target !== shown)
    setShown(target)
  const close = () => {
    setOpening(n => n + 1)
    onClose()
  }
  const mode = shown?.kind === 'edit'
    ? shown.mode
    : undefined
  return (
    <Dialog open={target !== undefined} onOpenChange={next => !next && !pending && close()}>
      <DialogContent finalFocus={finalFocus} className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {mode
              ? `Edit ${mode.name}`
              : 'Add a mode'}
          </DialogTitle>
          <DialogDescription>
            A turn&apos;s model, reasoning and budgets, the tools it offers and the words that switch to it by voice. A
            voice session reads its mode when it opens.
          </DialogDescription>
        </DialogHeader>
        <ModeForm
          key={`${opening}-${mode?.name ?? ''}`}
          mode={mode}
          taken={taken}
          onSave={onSave}
          onDone={close}
          onPendingChange={setPending}
        />
      </DialogContent>
    </Dialog>
  )
}
