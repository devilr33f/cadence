import type { FormEvent, RefObject } from 'react'
import type { Question } from '@/api/gen'
import { LoaderCircleIcon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { Choice } from '@/components/forms/choice'
import { describedBy } from '@/components/forms/fields/field-text'
import { FormField } from '@/components/forms/form-field'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { useSubscriptionActions } from '@/lib/subscriptions'
import { replyRefusal } from './reply-refusal'

// the server's limit, in characters
const MAX_MESSAGE = 2000

interface Errors {
  choice?: string
  message?: string
  form?: string
}

// a clarify question: one of its options (several when it allows them), or an answer of the owner's own words when it
// takes one (or offers no option); never both, as the server refuses that. sent with the question's token, so an
// answer to a question that changed is refused
export function QuestionReply({ deviceId, question, done }: {
  deviceId: string
  question: Question
  // where the focus goes once the answer went (the question, and this form with it, go too)
  done: RefObject<HTMLElement | null>
}) {
  const actions = useSubscriptionActions()
  const id = useId()
  const fieldRef = useRef<HTMLTextAreaElement>(null)
  const optionsRef = useRef<HTMLFieldSetElement>(null)
  const [chosen, setChosen] = useState<string[]>([])
  const [message, setMessage] = useState('')
  const [errors, setErrors] = useState<Errors>({})
  const [pending, setPending] = useState(false)
  const options = question.options ?? []
  const multi = question.multi === true
  const free = options.length === 0 || question.allowCustom === true

  function check(): Errors {
    const words = message.trim()
    if (chosen.length > 0 && words !== '')
      return { message: 'Choose an option or write an answer, not both.' }
    if (chosen.length === 0 && words === '') {
      return options.length > 0
        ? {
            choice: free ? 'Choose an option or write an answer.' : 'Choose an option.',
          }
        : { message: 'Required.' }
    }
    if ([...words].length > MAX_MESSAGE)
      return { message: `At most ${MAX_MESSAGE} characters.` }
    return {}
  }

  function focusError(found: Errors) {
    if (found.message)
      fieldRef.current?.focus()
    else if (found.choice)
      optionsRef.current?.querySelector('input')?.focus()
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found = check()
    setErrors(found)
    if (found.message || found.choice) {
      focusError(found)
      return
    }
    setPending(true)
    try {
      await actions.reply(chosen.length > 0
        ? { device: deviceId, token: question.token, answers: chosen }
        : {
            device: deviceId,
            token: question.token,
            message: message.trim(),
          })
      done.current?.focus()
    }
    catch (error) {
      setErrors({
        form: replyRefusal(error),
      })
    }
    finally {
      setPending(false)
    }
  }

  const choiceError = `${id}-choice`
  return (
    <form method="post" noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex max-w-[65ch] flex-col gap-4">
      {options.length > 0 && (
        <fieldset
          ref={optionsRef}
          className="flex flex-col gap-2"
          aria-describedby={errors.choice
            ? `${choiceError}-error`
            : undefined}
        >
          <legend className="text-sm font-medium">{multi ? 'Choose any' : 'Choose one'}</legend>
          {options.map(option => (
            <Choice
              key={option.id}
              id={`${id}-option-${option.id}`}
              type={multi ? 'checkbox' : 'radio'}
              name={`${id}-options`}
              checked={chosen.includes(option.id)}
              label={<span className="break-words whitespace-pre-wrap">{option.label}</span>}
              onChange={(checked) => {
                setChosen(list => multi
                  ? checked
                    ? [...list, option.id]
                    : list.filter(o => o !== option.id)
                  : [option.id])
                setErrors({})
              }}
            />
          ))}
          {errors.choice && <p id={`${choiceError}-error`} role="alert" className="text-sm text-destructive">{errors.choice}</p>}
          {chosen.length > 0 && free && (
            <Button type="button" variant="ghost" size="sm" className="self-start" onClick={() => setChosen([])}>
              Clear the choice
            </Button>
          )}
        </fieldset>
      )}
      {free && (
        <FormField
          id={id}
          label={options.length > 0
            ? 'Or your own answer'
            : 'Your answer'}
          error={errors.message}
        >
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
      )}
      {errors.form && <p role="alert" className="text-sm text-destructive">{errors.form}</p>}
      <Button type="submit" variant="secondary" className="self-start" aria-disabled={pending || undefined}>
        {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
        Send the answer
      </Button>
    </form>
  )
}
