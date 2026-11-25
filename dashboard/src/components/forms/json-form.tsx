import type { FormEvent, ReactNode } from 'react'
import type { JsonObject } from './json-pointer'
import type { SchemaFieldsHandle } from './schema-fields'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { firstControl, focusFirstInvalid } from './focus'
import { refusalOf } from './refusal'
import { SchemaFields } from './schema-fields'

export interface JsonFormProps {
  // undefined while the server sends no schema for it: the whole value is edited as json
  schema?: unknown
  initial: JsonObject
  // the form's name, and the json editor's label
  label: string
  submitLabel: string
  // resolves once the server took it; rejects with the server's answer, which the form words (a 422 at its field)
  onSubmit: (value: JsonObject) => Promise<void>
  onCancel?: () => void
  // what an object without fields says (a command without arguments)
  empty?: ReactNode
  // the form opens in place of what was focused: its first control takes the focus
  autoFocus?: boolean
}

// a value edited through the schema, or as raw json, in a form of its own with its submit (schema-fields.tsx)
export function JsonForm({ schema, initial, label, submitLabel, onSubmit, onCancel, empty, autoFocus = false }: JsonFormProps) {
  const formRef = useRef<HTMLFormElement>(null)
  const fieldsRef = useRef<SchemaFieldsHandle>(null)
  const [formError, setFormError] = useState<ReactNode>()
  const [focusTick, setFocusTick] = useState(0)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (autoFocus)
      firstControl(formRef.current)?.focus()
  }, [autoFocus])

  useEffect(() => {
    if (focusTick > 0)
      focusFirstInvalid(formRef.current)
  }, [focusTick])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const fields = fieldsRef.current
    if (pending || !fields)
      return
    setFormError(undefined)
    const body = fields.submit()
    if (!body) {
      setFocusTick(n => n + 1)
      return
    }
    setPending(true)
    try {
      await onSubmit(body)
      fields.settle()
    }
    catch (error) {
      const message = refusalOf(error, fields.refuse)
      if (message !== undefined)
        setFormError(message)
      setFocusTick(n => n + 1)
    }
    finally {
      setPending(false)
    }
  }

  return (
    <form method="post" ref={formRef} aria-label={label} noValidate onSubmit={e => void submit(e)} className="flex flex-col gap-5">
      <SchemaFields ref={fieldsRef} schema={schema} initial={initial} label={label} empty={empty} />
      {formError && <p role="alert" className="text-sm text-destructive">{formError}</p>}
      <div className="flex flex-wrap items-center gap-2">
        {/* the page's one yellow action is elsewhere: a form inside a card submits quieter */}
        <Button type="submit" variant="secondary" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          {submitLabel}
        </Button>
        {onCancel && <Button type="button" variant="ghost" disabled={pending} onClick={onCancel}>Cancel</Button>}
      </div>
    </form>
  )
}
