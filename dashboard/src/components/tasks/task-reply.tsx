import type { FormEvent, RefObject } from 'react'
import type { Question } from '@/lib/tasks'
import { LoaderCircleIcon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { describedBy } from '@/components/forms/fields/field-text'
import { FormField } from '@/components/forms/form-field'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { useTaskActions } from '@/lib/tasks'

// the server's limit, in characters
const MAX_MESSAGE = 2000

interface Errors {
  message?: string
  form?: string
}

// a 422 names the message (bare or as a pointer); a stale question, one already answered and the like are the form's,
// in codes
function refusalOf(error: unknown): Errors {
  if (error instanceof ApiError && error.status === 422 && error.path?.replace(/^\/?(?:body\/)?/, '') === 'message')
    return { message: `The server refused this value (${error.error}).` }
  return {
    form: describeError(error),
  }
}

// the answer to a clarify question, sent with the question's token so an answer to a question that changed is refused
export function TaskReply({ taskId, question, done }: {
  taskId: string
  question: Question
  // where the focus goes once the answer went (the question, and this form with it, go too)
  done: RefObject<HTMLElement | null>
}) {
  const actions = useTaskActions(taskId)
  const id = useId()
  const fieldRef = useRef<HTMLTextAreaElement>(null)
  const [message, setMessage] = useState('')
  const [errors, setErrors] = useState<Errors>({})
  const [pending, setPending] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found: Errors = message.trim() === ''
      ? { message: 'Required.' }
      : [...message].length > MAX_MESSAGE
          ? { message: `At most ${MAX_MESSAGE} characters.` }
          : {}
    setErrors(found)
    if (found.message) {
      fieldRef.current?.focus()
      return
    }
    setPending(true)
    try {
      await actions.reply(message, question.token)
      setMessage('')
      done.current?.focus()
    }
    catch (error) {
      const refusal = refusalOf(error)
      setErrors(refusal)
      if (refusal.message)
        fieldRef.current?.focus()
    }
    finally {
      setPending(false)
    }
  }

  const options = question.options
  return (
    <form method="post" noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex max-w-[65ch] flex-col gap-4">
      {options.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {options.map(option => (
            <Button
              key={option}
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                setMessage(option)
                setErrors({})
                fieldRef.current?.focus()
              }}
            >
              {`Use “${option}”`}
            </Button>
          ))}
        </div>
      )}
      <FormField id={id} label="Your reply" error={errors.message}>
        <Textarea
          ref={fieldRef}
          id={id}
          value={message}
          aria-invalid={errors.message ? true : undefined}
          aria-describedby={describedBy(id, { error: errors.message })}
          onChange={(e) => {
            setMessage(e.target.value)
            setErrors({})
          }}
        />
      </FormField>
      {errors.form && <p role="alert" className="text-sm text-destructive">{errors.form}</p>}
      <Button type="submit" variant="secondary" className="self-start" aria-disabled={pending || undefined}>
        {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
        Send the reply
      </Button>
    </form>
  )
}
