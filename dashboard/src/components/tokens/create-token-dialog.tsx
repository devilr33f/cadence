import type { RefObject } from 'react'
import type { CreatedToken, CreateTokenRequest } from '@/api/gen'
import { useState } from 'react'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { CreateTokenForm } from './create-token-form'
import { TokenOnce } from './token-once'

// the form, then the one-time token in its place. both go the moment the dialog starts closing, not after its fade, so
// a reopen in that window starts on an empty form; while the token shows, only Done closes the dialog, since a stray
// click beside it or Escape would throw it away for good
export function CreateTokenDialog({ open, onOpenChange, finalFocus, onCreate }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  // the page's own action: the empty state's button that may have opened it goes once the first token lands
  finalFocus: RefObject<HTMLElement | null>
  onCreate: (request: CreateTokenRequest) => Promise<CreatedToken>
}) {
  const [pending, setPending] = useState(false)
  const [created, setCreated] = useState<CreatedToken>()
  const [opening, setOpening] = useState(0)
  const shown = created !== undefined
  return (
    <Dialog
      open={open}
      disablePointerDismissal={shown}
      onOpenChange={(next, details) => {
        if (pending)
          return
        if (!next && shown && details.reason !== 'close-press') {
          details.cancel()
          return
        }
        if (!next) {
          setCreated(undefined)
          setOpening(n => n + 1)
        }
        onOpenChange(next)
      }}
    >
      <DialogContent finalFocus={finalFocus} showCloseButton={!shown}>
        {created
          ? <TokenOnce created={created} />
          : <CreateTokenForm key={opening} onCreate={onCreate} onCreated={setCreated} onPendingChange={setPending} />}
      </DialogContent>
    </Dialog>
  )
}
