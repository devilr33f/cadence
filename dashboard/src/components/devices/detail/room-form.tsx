import type { FormEvent } from 'react'
import type { Detail } from '@/api/gen'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { useDeviceActions } from '@/lib/devices'
import { roomError } from '../device-name'
import { RoomInput } from '../room-field'

interface Refusal {
  field: boolean
  message: string
}

function refusalOf(failure: unknown): Refusal {
  if (failure instanceof ApiError && failure.status === 422 && failure.error === 'invalid')
    return { field: true, message: 'The server refused this room (invalid).' }
  return {
    field: false,
    message: describeError(failure),
  }
}

// the device's room, edited in place as its name is: saved on Enter, put away on Escape or Cancel; an empty room is none
export function RoomForm({ device, onDone }: { device: Detail, onDone: () => void }) {
  const actions = useDeviceActions(device.id)
  const id = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const [room, setRoom] = useState(device.room)
  const [pending, setPending] = useState(false)
  const [refusal, setRefusal] = useState<Refusal>()

  useEffect(() => {
    inputRef.current?.select()
  }, [])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const trimmed = room.trim()
    if (trimmed === device.room) {
      onDone()
      return
    }
    const invalid = roomError(trimmed)
    if (invalid) {
      setRefusal({ field: true, message: invalid })
      inputRef.current?.focus()
      return
    }
    setPending(true)
    setRefusal(undefined)
    try {
      await actions.setRoom(trimmed)
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

  return (
    <form method="post" noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex w-full flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Label htmlFor={`${id}-room`}>Room</Label>
        <RoomInput
          ref={inputRef}
          id={`${id}-room`}
          value={room}
          autoFocus
          placeholder="No room"
          aria-invalid={refusal?.field === true || undefined}
          aria-describedby={refusal
            ? `${id}-error`
            : undefined}
          className="w-full sm:w-64"
          onChange={e => setRoom(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape' && !pending) {
              e.preventDefault()
              onDone()
            }
          }}
        />
        <Button type="submit" variant="secondary" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          Save the room
        </Button>
        <Button type="button" variant="ghost" disabled={pending} onClick={onDone}>Cancel</Button>
      </div>
      {refusal && <p id={`${id}-error`} role="alert" className="text-sm text-destructive">{refusal.message}</p>}
    </form>
  )
}
