import type { RefObject } from 'react'
import type { MemoryValues } from './memory-body'
import type { DeviceView, MemoryJson } from '@/api/gen'
import { useRef, useState } from 'react'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { Button } from '@/components/ui/button'
import { useMemoryActions } from '@/lib/memory'
import { editBody, valuesOf } from './memory-body'
import { MemoryDialog } from './memory-dialog'
import { short } from './memory-words'

// edits an active memory in a dialog its own button opens, which takes the focus back; an edit that moves the memory out
// of the table's filter takes its row away, so the focus then goes to the page's action
export function EditMemoryButton({ memory, devices, stays, finalFocus }: {
  memory: MemoryJson
  devices: readonly DeviceView[]
  // whether the memory, as edited, is still among the rows the table shows
  stays: (values: MemoryValues) => boolean
  finalFocus: RefObject<HTMLElement | null>
}) {
  const actions = useMemoryActions()
  const [open, setOpen] = useState(false)
  const movedRef = useRef(false)
  return (
    <MemoryDialog
      open={open}
      onOpenChange={(next) => {
        if (next)
          movedRef.current = false
        setOpen(next)
      }}
      finalFocus={() => movedRef.current ? finalFocus.current : true}
      trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Edit: ${short(memory.text)}`}>Edit</Button>}
      title="Edit the memory"
      description="A learned memory you edit becomes yours: learning no longer changes it."
      initial={valuesOf(memory)}
      devices={devices}
      withPin
      submitLabel="Save the memory"
      onSubmit={async (values) => {
        const body = editBody(memory, values)
        if (Object.keys(body).length === 0)
          return
        await actions.edit(memory.id, body)
        movedRef.current = !stays(values)
      }}
    />
  )
}

export function DeleteMemoryButton({ memory, finalFocus }: { memory: MemoryJson, finalFocus: RefObject<HTMLElement | null> }) {
  const actions = useMemoryActions()
  return (
    <ConfirmDialog
      trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Delete: ${short(memory.text)}`}>Delete</Button>}
      title="Delete this memory?"
      description={<span className="break-words whitespace-pre-wrap">{memory.text}</span>}
      confirmLabel="Delete the memory"
      tone="destructive"
      inlineError
      finalFocus={finalFocus}
      onConfirm={() => actions.remove(memory.id)}
    />
  )
}
