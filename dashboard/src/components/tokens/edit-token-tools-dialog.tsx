import type { RefObject } from 'react'
import type { TokenView } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { LoaderCircleIcon } from 'lucide-react'
import { useState } from 'react'
import { describeError } from '@/api/describe-error'
import { useApi } from '@/api/use-api'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { mcpToolsQueryOptions, servedTools } from '@/lib/tokens'
import { TokenToolsField } from './token-tools-field'

// a token's MCP tool list, edited alone: the token itself never changes, so
// its value is never shown again
export function EditTokenToolsDialog({ token, open, onOpenChange, onSave, finalFocus }: {
  token: TokenView
  open: boolean
  onOpenChange: (open: boolean) => void
  onSave: (name: string, tools: string[]) => Promise<void>
  finalFocus?: RefObject<HTMLButtonElement | null>
}) {
  const api = useApi()
  const mcpTools = useQuery({
    ...mcpToolsQueryOptions(api),
    enabled: open,
  })
  // the token's own list seeds the choice; the dialog is keyed by its token,
  // so each open starts from the stored one
  const [tools, setTools] = useState<string[]>(() => [
    ...(token.mcpTools ?? []),
  ])
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  async function save() {
    if (pending)
      return
    setPending(true)
    setError('')
    try {
      await onSave(token.name, servedTools(mcpTools.data, tools))
      onOpenChange(false)
    }
    catch (cause) {
      setError(describeError(cause))
    }
    finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent finalFocus={finalFocus}>
        <DialogHeader>
          <DialogTitle>{`Tools of ${token.name}`}</DialogTitle>
          <DialogDescription>
            The MCP tools this token may call; every tool unless some are named. The change applies at once, to the
            tool list its clients see and to every call.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <TokenToolsField id="token-tools" tools={mcpTools.data} value={tools} onChange={setTools} />
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
          <Button type="button" aria-disabled={pending || undefined} onClick={() => void save()}>
            {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
            Save the tools
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
