import type { FormEvent } from 'react'
import type { MemoryValues } from './memory-body'
import type { MemoryField } from './memory-refusal'
import type { DeviceView } from '@/api/gen'
import type { MemoryKind } from '@/lib/memory'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { Choice } from '@/components/forms/choice'
import { describedBy } from '@/components/forms/fields/field-text'
import { focusFirstInvalid } from '@/components/forms/focus'
import { FormField } from '@/components/forms/form-field'
import { Button } from '@/components/ui/button'
import { DialogClose, DialogFooter } from '@/components/ui/dialog'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { MEMORY_KINDS } from '@/lib/memory'
import { GLOBAL } from './memory-body'
import { memoryRefusal } from './memory-refusal'
import { kindLabel } from './memory-words'

// the server's limit, in characters
const MAX_TEXT = 500

type Errors = Partial<Record<MemoryField, string>>

function check(values: MemoryValues): Errors {
  if (values.text.trim() === '')
    return { text: 'Required.' }
  if ([...values.text].length > MAX_TEXT)
    return { text: `At most ${MAX_TEXT} characters.` }
  return {}
}

// a memory's text, kind and scope (and its pin, when it is edited); the server checks the text again, for a secret too
export function MemoryForm({ initial, devices, withPin = false, submitLabel, onSubmit, onDone, onPendingChange }: {
  initial: MemoryValues
  devices: readonly DeviceView[]
  withPin?: boolean
  submitLabel: string
  onSubmit: (values: MemoryValues) => Promise<void>
  onDone: () => void
  onPendingChange: (pending: boolean) => void
}) {
  const baseId = useId()
  const formRef = useRef<HTMLFormElement>(null)
  const [values, setValues] = useState(initial)
  const [errors, setErrors] = useState<Errors>({})
  const [pending, setPending] = useState(false)
  const [focusTick, setFocusTick] = useState(0)

  useEffect(() => {
    if (focusTick > 0)
      focusFirstInvalid(formRef.current)
  }, [focusTick])

  const change = (field: Exclude<MemoryField, 'form'>, next: Partial<MemoryValues>) => {
    setValues(current => ({
      ...current,
      ...next,
    }))
    setErrors(({ [field]: _, form: __, ...rest }) => rest)
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found = check(values)
    setErrors(found)
    if (Object.keys(found).length > 0) {
      setFocusTick(n => n + 1)
      return
    }
    setPending(true)
    onPendingChange(true)
    try {
      await onSubmit(values)
      onDone()
    }
    catch (error) {
      const refusal = memoryRefusal(error)
      setErrors({ [refusal.field]: refusal.message })
      setFocusTick(n => n + 1)
    }
    finally {
      setPending(false)
      onPendingChange(false)
    }
  }

  const id = (field: string) => `${baseId}-${field}`
  const textDescription = `What the assistant should know, in a sentence; at most ${MAX_TEXT} characters. Never a password or a key.`
  // a kind or a speaker the page does not list (newer, or deleted) stays choosable as it is
  const kinds: string[] = MEMORY_KINDS.includes(initial.kind)
    ? [...MEMORY_KINDS]
    : [...MEMORY_KINDS, initial.kind]
  const unknownScope = initial.scope !== GLOBAL && !devices.some(d => d.id === initial.scope)
  return (
    <form method="post" ref={formRef} noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex flex-col gap-5">
      <FormField id={id('text')} label="Text" required description={textDescription} error={errors.text}>
        <Textarea
          id={id('text')}
          value={values.text}
          aria-required
          aria-invalid={errors.text ? true : undefined}
          aria-describedby={describedBy(id('text'), { description: true, error: errors.text })}
          onChange={e => change('text', { text: e.target.value })}
        />
      </FormField>
      <FormField id={id('kind')} label="Kind" required error={errors.kind}>
        <NativeSelect
          id={id('kind')}
          className="w-full"
          value={values.kind}
          aria-required
          aria-invalid={errors.kind ? true : undefined}
          aria-describedby={describedBy(id('kind'), { error: errors.kind })}
          onChange={e => change('kind', { kind: e.target.value as MemoryKind })}
        >
          {kinds.map(kind => <NativeSelectOption key={kind} value={kind}>{kindLabel(kind)}</NativeSelectOption>)}
        </NativeSelect>
      </FormField>
      <FormField id={id('scope')} label="Scope" required error={errors.scope}>
        <NativeSelect
          id={id('scope')}
          className="w-full"
          value={values.scope}
          aria-required
          aria-invalid={errors.scope ? true : undefined}
          aria-describedby={describedBy(id('scope'), { error: errors.scope })}
          onChange={e => change('scope', { scope: e.target.value })}
        >
          <NativeSelectOption value={GLOBAL}>Global (every speaker)</NativeSelectOption>
          {devices.map(device => <NativeSelectOption key={device.id} value={device.id}>{device.name}</NativeSelectOption>)}
          {unknownScope && <NativeSelectOption value={initial.scope}>{initial.scope}</NativeSelectOption>}
        </NativeSelect>
      </FormField>
      {withPin && (
        <Choice
          id={id('pinned')}
          checked={values.pinned}
          onChange={pinned => setValues(current => ({ ...current, pinned }))}
          label="Pinned"
          description="A pinned memory reaches every turn of its scope, and learning never changes it."
        />
      )}
      {errors.form && <p role="alert" className="text-sm text-destructive">{errors.form}</p>}
      <DialogFooter>
        <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
        <Button type="submit" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          {submitLabel}
        </Button>
      </DialogFooter>
    </form>
  )
}
