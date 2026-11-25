import type { RefObject } from 'react'
import type { DeviceView, MemoryJson } from '@/api/gen'
import { useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { Button } from '@/components/ui/button'
import { useMemoryActions } from '@/lib/memory'
import { editBody, valuesOf } from './memory-body'
import { MemoryDialog } from './memory-dialog'
import { changeOf, short } from './memory-words'

// an approval or a rejection that went through takes its row away: the focus waits on the section's heading. one the
// server refused is worded beside the table (its code), the focus kept on the button, which is never disabled under it
function useSettled(heading: RefObject<HTMLElement | null>, onRefused: (message?: string) => void) {
  const [pending, setPending] = useState(false)
  async function run(action: () => Promise<void>, failure: string) {
    if (pending)
      return
    setPending(true)
    onRefused(undefined)
    try {
      await action()
      heading.current?.focus()
    }
    catch (error) {
      onRefused(`${failure} ${describeError(error)}`)
    }
    finally {
      setPending(false)
    }
  }
  return { pending, run }
}

// what a suggestion allows: approve (a deletion after asking) or reject; a new memory may be edited first, a proposal is
// taken as made (the server edits none)
export function SuggestionActions({ memory, devices, heading, onRefused }: {
  memory: MemoryJson
  devices: readonly DeviceView[]
  heading: RefObject<HTMLElement | null>
  // a refusal's words for beside the table, or nothing once another try starts
  onRefused: (message?: string) => void
}) {
  const actions = useMemoryActions()
  const settled = useSettled(heading, onRefused)
  const [editing, setEditing] = useState(false)
  const approvedRef = useRef(false)
  const name = short(memory.text)
  const change = changeOf(memory)

  const approve = change === 'delete'
    ? (
        <ConfirmDialog
          trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Approve: ${name}`}>Approve</Button>}
          title="Delete the memory it names?"
          description={(
            <span className="flex flex-col gap-2">
              <span>Learning proposes to forget this memory. Approving deletes it for good:</span>
              <span className="block rounded-md border bg-muted/40 p-3 break-words whitespace-pre-wrap text-foreground">{memory.text}</span>
            </span>
          )}
          confirmLabel="Approve the deletion"
          tone="destructive"
          inlineError
          finalFocus={heading}
          onConfirm={() => actions.approve(memory.id)}
        />
      )
    : (
        <Button type="button" variant="ghost" size="sm" aria-label={`Approve: ${name}`} aria-disabled={settled.pending || undefined} onClick={() => void settled.run(() => actions.approve(memory.id), 'The suggestion was not approved.')}>
          Approve
        </Button>
      )

  return (
    <div className="flex justify-end gap-1">
      {approve}
      {change === 'new' && (
        <MemoryDialog
          open={editing}
          onOpenChange={(open) => {
            if (open)
              approvedRef.current = false
            setEditing(open)
          }}
          trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Edit and approve: ${name}`}>Edit</Button>}
          title="Edit and approve"
          description="Change what learning proposed, then keep it as an active memory."
          // approved, its row goes: the focus waits on the heading; cancelled, it goes back to the button
          finalFocus={() => approvedRef.current ? heading.current : true}
          initial={valuesOf(memory)}
          devices={devices}
          submitLabel="Approve with these changes"
          onSubmit={async (values) => {
            await actions.editAndApprove(memory.id, editBody(memory, values))
            approvedRef.current = true
          }}
        />
      )}
      <Button type="button" variant="ghost" size="sm" aria-label={`Reject: ${name}`} aria-disabled={settled.pending || undefined} onClick={() => void settled.run(() => actions.reject(memory.id), 'The suggestion was not rejected.')}>
        Reject
      </Button>
    </div>
  )
}
