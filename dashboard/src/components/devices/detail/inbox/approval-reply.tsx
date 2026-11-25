import type { FormEvent, RefObject } from 'react'
import type { Question } from '@/api/gen'
import type { Decision } from '@/lib/subscriptions'
import { LoaderCircleIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { Choice } from '@/components/forms/choice'
import { Button } from '@/components/ui/button'
import { useSubscriptionActions } from '@/lib/subscriptions'
import { replyRefusal } from './reply-refusal'

// deny first and chosen until another is picked: the keyboard lands on it, and a stray send approves nothing
const DECISIONS: readonly { value: Decision, label: string, description: string }[] = [
  { value: 'deny', label: 'Deny', description: 'The system that asked does not do it.' },
  { value: 'once', label: 'Approve once', description: 'This request alone.' },
  { value: 'session', label: 'Approve for the session', description: 'This request and the same kind for the rest of the system\'s session.' },
]

const SEND_TEXT: Record<Decision, string> = {
  deny: 'Send the denial',
  once: 'Approve once',
  session: 'Approve for the session',
}

// an approval's answer, with the evidence dashboard; sent with the token the form opened on (the form is keyed by it)
export function ApprovalReply({ deviceId, question, done }: {
  deviceId: string
  question: Question
  done: RefObject<HTMLElement | null>
}) {
  const actions = useSubscriptionActions()
  const id = useId()
  const [decision, setDecision] = useState<Decision>('deny')
  const [error, setError] = useState<string>()
  const [pending, setPending] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    setPending(true)
    setError(undefined)
    try {
      await actions.reply({ device: deviceId, token: question.token, decision })
      done.current?.focus()
    }
    catch (failure) {
      setError(replyRefusal(failure))
    }
    finally {
      setPending(false)
    }
  }

  return (
    <form method="post" noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex max-w-[65ch] flex-col gap-4">
      <fieldset className="flex flex-col gap-2">
        <legend className="text-sm font-medium">Decision</legend>
        {DECISIONS.map(choice => (
          <Choice
            key={choice.value}
            id={`${id}-${choice.value}`}
            type="radio"
            name={`${id}-decision`}
            checked={decision === choice.value}
            label={choice.label}
            description={choice.description}
            onChange={() => {
              setDecision(choice.value)
              setError(undefined)
            }}
          />
        ))}
      </fieldset>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      <Button
        type="submit"
        variant={decision === 'deny'
          ? 'outline'
          : 'secondary'}
        className="self-start"
        aria-disabled={pending || undefined}
      >
        {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
        {SEND_TEXT[decision]}
      </Button>
    </form>
  )
}
