import type { FormEvent } from 'react'
import type { Hints, PatchToolRequest, ToolView } from '@/api/gen'
import { LoaderCircleIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { Choice } from '@/components/forms/choice'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'

interface Access {
  effects: 'read' | 'write'
  trust: 'trusted' | 'untrusted'
  enabledDefault: boolean
  anyTurn: boolean
}

function accessOf(tool: ToolView): Access {
  return {
    effects: tool.effects === 'read'
      ? 'read'
      : 'write',
    trust: tool.trust === 'trusted'
      ? 'trusted'
      : 'untrusted',
    enabledDefault: tool.enabledDefault,
    anyTurn: tool.anyTurn,
  }
}

const HINTS: readonly [keyof Hints, string][] = [
  ['readOnly', 'read-only'],
  ['destructive', 'destructive'],
  ['idempotent', 'idempotent'],
  ['openWorld', 'open world'],
]

function hintsText(hints: Hints): string {
  const set = HINTS.filter(([key]) => hints[key]).map(([, words]) => words)
  return set.length > 0
    ? set.join(', ')
    : 'none'
}

// only the overrides the owner changed
function patchOf(tool: ToolView, next: Access): PatchToolRequest {
  const was = accessOf(tool)
  return Object.fromEntries((Object.keys(next) as (keyof Access)[])
    .filter(key => next[key] !== was[key])
    .map(key => [key, next[key]]))
}

function AccessForm({ tool, onSave, onDone, onPendingChange }: {
  tool: ToolView
  onSave: (patch: PatchToolRequest) => Promise<void>
  onDone: () => void
  onPendingChange: (pending: boolean) => void
}) {
  const id = useId()
  const [access, setAccess] = useState(() => accessOf(tool))
  const [error, setError] = useState<string>()
  const [pending, setPending] = useState(false)
  const set = (next: Partial<Access>) => {
    setAccess(current => ({
      ...current,
      ...next,
    }))
    setError(undefined)
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const patch = patchOf(tool, access)
    if (Object.keys(patch).length === 0) {
      onDone()
      return
    }
    setPending(true)
    onPendingChange(true)
    try {
      await onSave(patch)
      onDone()
    }
    catch (failure) {
      setError(describeError(failure))
    }
    finally {
      setPending(false)
      onPendingChange(false)
    }
  }

  return (
    <form method="post" noValidate onSubmit={e => void submit(e)} className="flex flex-col gap-5">
      <fieldset className="flex flex-col gap-3">
        <legend className="mb-2 text-sm font-medium">Effects</legend>
        <Choice id={`${id}-read`} type="radio" name={`${id}-effects`} checked={access.effects === 'read'} onChange={() => set({ effects: 'read' })} label="Read" description="It only reads: it may run in any turn, a follow-up or an event turn too." />
        <Choice id={`${id}-write`} type="radio" name={`${id}-effects`} checked={access.effects === 'write'} onChange={() => set({ effects: 'write' })} label="Write" description="It may change something: it needs a turn a wake or a press began." />
      </fieldset>
      <fieldset className="flex flex-col gap-3">
        <legend className="mb-2 text-sm font-medium">Trust</legend>
        <Choice id={`${id}-untrusted`} type="radio" name={`${id}-trust`} checked={access.trust === 'untrusted'} onChange={() => set({ trust: 'untrusted' })} label="Untrusted" description="After its result the rest of the turn offers read tools only." />
        <Choice id={`${id}-trusted`} type="radio" name={`${id}-trust`} checked={access.trust === 'trusted'} onChange={() => set({ trust: 'trusted' })} label="Trusted" description="Marked read too, its result leaves the turn untainted." />
      </fieldset>
      <Choice id={`${id}-default`} checked={access.enabledDefault} onChange={enabledDefault => set({ enabledDefault })} label="Offer it to every speaker without a tools list" description="Off: only a speaker whose tools setting names it gets it." />
      <Choice id={`${id}-any`} checked={access.anyTurn} onChange={anyTurn => set({ anyTurn })} label="Allow it in any turn" description="A write tool then runs in a follow-up too; never in an event turn." />
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      <DialogFooter>
        <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
        <Button type="submit" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          Save the access
        </Button>
      </DialogFooter>
    </form>
  )
}

// a tool's overrides (deploy/server/README.md, MCP servers): what it does, whether its result is trusted, who gets it and
// in which turns; the dialog's own button takes the focus back
export function ToolAccessDialog({ tool, onSave }: { tool: ToolView, onSave: (patch: PatchToolRequest) => Promise<void> }) {
  const [open, setOpen] = useState(false)
  const [pending, setPending] = useState(false)
  const [opening, setOpening] = useState(0)
  const change = (next: boolean) => {
    if (!next)
      setOpening(n => n + 1)
    setOpen(next)
  }
  return (
    <Dialog open={open} onOpenChange={next => !pending && change(next)}>
      <DialogTrigger render={<Button type="button" variant="ghost" size="sm" aria-label={`Edit access: ${tool.local}`}>Access</Button>} />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{`Access of ${tool.local}`}</DialogTitle>
          <DialogDescription>{`The server's hints (${hintsText(tool.hints)}) are shown, never applied: these overrides decide.`}</DialogDescription>
        </DialogHeader>
        <AccessForm key={opening} tool={tool} onSave={onSave} onDone={() => change(false)} onPendingChange={setPending} />
      </DialogContent>
    </Dialog>
  )
}
