import type { FormEvent } from 'react'
import type { BoardView, CreateOutBody } from '@/api/gen'
import { useQueryClient } from '@tanstack/react-query'
import { LoaderCircleIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { createDevice } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { RequiredMark } from '@/components/forms/fields/field-shell'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { devicesQueryOptions } from '@/lib/devices'
import { nameError } from '../device-name'

export interface EnrollChoice {
  board: string
  name: string
}

// the sdk is called directly: a mutation would keep the answer, and the code in it, in the cache
export function EnrollForm({ boards, initial, onCreated }: {
  boards: readonly BoardView[]
  initial?: EnrollChoice
  onCreated: (created: CreateOutBody, choice: EnrollChoice) => void
}) {
  const api = useApi()
  const queryClient = useQueryClient()
  const id = useId()
  const [board, setBoard] = useState(initial?.board ?? boards[0]?.id ?? '')
  const [name, setName] = useState(initial?.name ?? '')
  const [error, setError] = useState<{ field: 'name' | 'form', message: string }>()
  const [pending, setPending] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const trimmed = name.trim()
    const invalid = nameError(trimmed)
    if (invalid) {
      setError({ field: 'name', message: invalid })
      return
    }
    setError(undefined)
    setPending(true)
    try {
      const { data } = await createDevice({
        client: api,
        body: { model: board, name: trimmed },
        throwOnError: true,
      })
      onCreated(data, { board, name: trimmed })
    }
    catch (failure) {
      setError({
        field: 'form',
        message: describeError(failure),
      })
      setPending(false)
    }
    finally {
      // after any answer: one lost on its way may still have made the device
      void queryClient.invalidateQueries({ queryKey: devicesQueryOptions(api).queryKey })
    }
  }

  const nameInvalid = error?.field === 'name'
  return (
    <form method="post" noValidate onSubmit={e => void submit(e)} className="flex max-w-md flex-col gap-5">
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-board`}>Board</Label>
        <NativeSelect id={`${id}-board`} className="w-full" value={board} onChange={e => setBoard(e.target.value)}>
          {boards.map(b => <NativeSelectOption key={b.id} value={b.id}>{b.name}</NativeSelectOption>)}
        </NativeSelect>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-name`}>
          Name
          <RequiredMark required />
        </Label>
        <Input
          id={`${id}-name`}
          // back from an expired code, the name it had is where the reader picks up
          autoFocus={initial !== undefined}
          value={name}
          autoComplete="off"
          placeholder="Kitchen"
          aria-required="true"
          aria-invalid={nameInvalid || undefined}
          aria-describedby={nameInvalid
            ? `${id}-name-error`
            : undefined}
          onChange={e => setName(e.target.value)}
        />
        {nameInvalid && <p id={`${id}-name-error`} role="alert" className="text-sm text-destructive">{error.message}</p>}
      </div>
      {error?.field === 'form' && <p role="alert" className="text-sm text-destructive">{error.message}</p>}
      <div>
        {/* focusable while it works, so a refusal finds the focus where it was */}
        <Button type="submit" size="lg" disabled={boards.length === 0} aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          Make an enroll code
        </Button>
      </div>
    </form>
  )
}
