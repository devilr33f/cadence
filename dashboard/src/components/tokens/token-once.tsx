import type { CreatedToken } from '@/api/gen'
import { TriangleAlertIcon } from 'lucide-react'
import { useId } from 'react'
import { CopyButton } from '@/components/data/copy-button'
import { Button } from '@/components/ui/button'
import { DialogClose, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'

// the one time the token is shown: it lives in the dialog's state alone (no cache, storage, url or log) and goes with
// the dialog, closed however it is
export function TokenOnce({ created }: { created: CreatedToken }) {
  const captionId = useId()
  return (
    <>
      <DialogHeader>
        <DialogTitle>Copy the new token</DialogTitle>
        <DialogDescription>{`${created.name} holds ${created.scopes.join(', ')}.`}</DialogDescription>
      </DialogHeader>
      <p className="flex items-start gap-2 rounded-md border bg-warning-surface p-3 text-sm">
        <TriangleAlertIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-warning" />
        It won't be shown again: the server keeps only its hash. Copy it now into whatever will use it.
      </p>
      <figure aria-labelledby={captionId} className="flex flex-col gap-2">
        <figcaption id={captionId} className="text-sm font-medium">{`Token for ${created.name}`}</figcaption>
        <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 font-mono text-xs/5 break-all whitespace-pre-wrap select-all"><code>{created.token}</code></pre>
      </figure>
      <CopyButton text={created.token} label="Copy the token" autoFocus />
      <DialogFooter>
        <DialogClose render={<Button type="button" variant="secondary" />}>Done</DialogClose>
      </DialogFooter>
    </>
  )
}
