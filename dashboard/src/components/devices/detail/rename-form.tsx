import type { FormEvent } from 'react'
import type { Detail } from '@/api/gen'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useDeviceActions } from '@/lib/devices'
import { nameError } from '../device-name'

interface Refusal {
  field: boolean
  message: string
}

// a refusal of the name itself lands on the field, in the server's code; any other is worded beside the button
function refusalOf(failure: unknown): Refusal {
  if (failure instanceof ApiError && failure.status === 409 && failure.error === 'name_taken')
    return { field: true, message: 'Another speaker has this name, in any case (name_taken).' }
  if (failure instanceof ApiError && failure.status === 422 && failure.error === 'invalid')
    return { field: true, message: 'The server refused this name (invalid).' }
  return {
    field: false,
    message: describeError(failure),
  }
}

// the device's name in place of its title: trimmed and checked as the server checks it, saved on Enter, put away on
// Escape or Cancel (the same name sends nothing)
export function RenameForm({ device, onDone }: { device: Detail, onDone: () => void }) {
  const actions = useDeviceActions(device.id)
  const id = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState(device.name)
  const [pending, setPending] = useState(false)
  const [refusal, setRefusal] = useState<Refusal>()

  // the whole name, ready to be typed over
  useEffect(() => {
    inputRef.current?.select()
  }, [])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const trimmed = name.trim()
    if (trimmed === device.name) {
      onDone()
      return
    }
    const invalid = nameError(trimmed)
    if (invalid) {
      setRefusal({ field: true, message: invalid })
      inputRef.current?.focus()
      return
    }
    setPending(true)
    setRefusal(undefined)
    try {
      await actions.rename(trimmed)
      onDone()
    }
    catch (failure) {
      const refused = refusalOf(failure)
      setRefusal(refused)
      setPending(false)
      if (refused.field)
        inputRef.current?.focus()
    }
  }

  const fieldInvalid = refusal?.field === true
  return (
    <form method="post" noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex w-full flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Label htmlFor={`${id}-name`} className="sr-only">Name</Label>
        <Input
          ref={inputRef}
          id={`${id}-name`}
          value={name}
          autoFocus
          aria-invalid={fieldInvalid || undefined}
          aria-describedby={refusal
            ? `${id}-error`
            : undefined}
          className="w-full text-lg font-semibold sm:w-80"
          onChange={e => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape' && !pending) {
              e.preventDefault()
              onDone()
            }
          }}
        />
        {/* focusable while it works, so a refusal finds the focus where it was */}
        <Button type="submit" variant="secondary" className="h-9" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          Save the name
        </Button>
        <Button type="button" variant="ghost" className="h-9" disabled={pending} onClick={onDone}>Cancel</Button>
      </div>
      {refusal && <p id={`${id}-error`} role="alert" className="text-sm text-destructive">{refusal.message}</p>}
    </form>
  )
}
