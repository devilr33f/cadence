import type { FormEvent } from 'react'
import type { SecretView } from '@/api/gen'
import { KeyRoundIcon, LoaderCircleIcon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { deleteDeviceSecret, putDeviceSecret } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { StatusBadge } from '@/components/data/status-badge'
import { SecretInput } from '@/components/forms/secret-input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { toast } from '@/components/ui/toast-manager'
import { useFocusReturn } from '@/hooks/use-focus-return'
import { useRefreshDevice } from './use-refresh-device'

interface Where {
  deviceId: string
  capability: string
}

// write-only: the value goes from an uncontrolled field to the request and nowhere else (no state, no cache, the field
// cleared as soon as it is read); the page only ever learns whether one is set
function ReplaceForm({ where, secret, onDone }: { where: Where, secret: SecretView, onDone: () => void }) {
  const api = useApi()
  const refresh = useRefreshDevice(where.deviceId)
  const id = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const value = new FormData(form).get('value')
    form.reset()
    if (pending)
      return
    if (typeof value !== 'string' || value === '') {
      setError('Type the new value.')
      inputRef.current?.focus()
      return
    }
    setPending(true)
    setError(undefined)
    try {
      await putDeviceSecret({
        client: api,
        path: { id: where.deviceId, cap: where.capability, name: secret.name },
        body: { value },
        throwOnError: true,
      })
      onDone()
      await refresh()
    }
    catch (failure) {
      setError(describeError(failure))
      setPending(false)
      inputRef.current?.focus()
    }
  }

  return (
    <form method="post" autoComplete="off" onSubmit={e => void submit(e)} className="flex w-full flex-col gap-2 pt-1">
      <Label htmlFor={`${id}-value`}>{`New value for ${secret.name}`}</Label>
      <div className="flex flex-wrap gap-2">
        <SecretInput
          ref={inputRef}
          id={`${id}-value`}
          name="value"
          autoFocus
          className="w-full sm:w-72"
          aria-invalid={error ? true : undefined}
          aria-describedby={error
            ? `${id}-error`
            : undefined}
        />
        <Button type="submit" variant="secondary" className="h-9" disabled={pending}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          Save the secret
        </Button>
        <Button type="button" variant="ghost" className="h-9" disabled={pending} onClick={onDone}>Cancel</Button>
      </div>
      {error && <p id={`${id}-error`} role="alert" className="text-sm text-destructive">{error}</p>}
    </form>
  )
}

export function SecretRow({ where, secret }: { where: Where, secret: SecretView }) {
  const api = useApi()
  const refresh = useRefreshDevice(where.deviceId)
  const nameId = useId()
  const [replacing, setReplacing] = useState(false)
  // Set/Replace stays the row's anchor: the form hands the focus back to it, and so does a delete, whose own button
  // goes with the secret
  const back = useFocusReturn<HTMLButtonElement>(replacing)

  async function remove() {
    try {
      await deleteDeviceSecret({
        client: api,
        path: { id: where.deviceId, cap: where.capability, name: secret.name },
        throwOnError: true,
      })
    }
    catch (failure) {
      toast.add({
        type: 'error',
        title: `${secret.name} was not deleted`,
        description: describeError(failure),
      })
      throw failure
    }
    // read again once the dialog has closed, so it is not unmounted with the delete button while still open
    void refresh()
  }

  return (
    <li aria-labelledby={nameId} className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b py-2.5 last:border-b-0">
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="flex items-center gap-2">
          <KeyRoundIcon aria-hidden="true" className="size-3.5 text-muted-foreground" />
          <span id={nameId} className="font-mono text-xs">{secret.name}</span>
        </span>
        {secret.description && <span className="text-sm text-muted-foreground">{secret.description}</span>}
      </div>
      {secret.set
        ? <StatusBadge tone="success" label="Set" />
        : <StatusBadge tone="neutral" label="Not set" />}
      <div className="flex gap-1">
        {!replacing && (
          <Button
            ref={back.ref}
            type="button"
            variant="outline"
            size="sm"
            aria-label={`${secret.set ? 'Replace' : 'Set'} ${secret.name}`}
            onClick={() => setReplacing(true)}
          >
            {secret.set ? 'Replace' : 'Set'}
          </Button>
        )}
        {secret.set && (
          <ConfirmDialog
            trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Delete ${secret.name}`}>Delete</Button>}
            title={`Delete ${secret.name}?`}
            description="The device loses it at once; what needs it stops working until a new one is set."
            confirmLabel="Delete the secret"
            tone="destructive"
            finalFocus={back.ref}
            onConfirm={remove}
          />
        )}
      </div>
      {replacing && (
        <ReplaceForm
          where={where}
          secret={secret}
          onDone={() => {
            back.arm()
            setReplacing(false)
          }}
        />
      )}
    </li>
  )
}
