import type { FormEvent } from 'react'
import type { SubscriptionErrors } from './subscription-refusal'
import type { CreateSubscriptionRequest, DeviceView, Instance, Subscription } from '@/api/gen'
import type { Delivery, SubscriptionEvent, Until } from '@/lib/subscriptions'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { Choice } from '@/components/forms/choice'
import { FieldMessage } from '@/components/forms/fields/field-shell'
import { describedBy } from '@/components/forms/fields/field-text'
import { focusFirstInvalid } from '@/components/forms/focus'
import { FormField } from '@/components/forms/form-field'
import { Button } from '@/components/ui/button'
import { DialogClose, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { DELIVERY_TEXT, EVENT_TEXT, EVENTS, UNTIL_TEXT } from '@/lib/subscriptions'
import { checkTopic, subscriptionRefusal } from './subscription-refusal'
import { TopicPicker } from './topic-picker'

const DEFAULT_DELIVERY = 'default'
type DeliveryChoice = Delivery | typeof DEFAULT_DELIVERY

const DELIVERY_CHOICES: readonly { value: DeliveryChoice, label: string, description: string }[] = [
  { value: DEFAULT_DELIVERY, label: 'The speaker\'s default', description: 'As its subscriptionDelivery setting says.' },
  { value: 'speaker', label: DELIVERY_TEXT.speaker, description: 'A fixed sentence names the update; no model reads it.' },
  { value: 'agent', label: DELIVERY_TEXT.agent, description: 'The assistant reads the update and may say something, or nothing.' },
  { value: 'mute', label: DELIVERY_TEXT.mute, description: 'Nothing is said: silences a narrower topic inside a wider one.' },
]

const UNTIL_CHOICES: readonly { value: Until, description: string }[] = [
  { value: 'unsubscribed', description: 'Until you or the speaker removes it.' },
  { value: 'next_stop', description: 'Ends with the first stop it tells.' },
]

export function SubscriptionForm({ devices, instances, initialDevice, onCreate, onCreated, onPendingChange }: {
  devices: readonly DeviceView[]
  instances: readonly Instance[]
  initialDevice?: string
  onCreate: (request: CreateSubscriptionRequest) => Promise<Subscription>
  onCreated: () => void
  onPendingChange: (pending: boolean) => void
}) {
  const baseId = useId()
  const formRef = useRef<HTMLFormElement>(null)
  const topicRef = useRef<HTMLInputElement>(null)
  const [device, setDevice] = useState(initialDevice ?? devices[0]?.id ?? '')
  const [topic, setTopic] = useState('')
  const [events, setEvents] = useState<SubscriptionEvent[]>([])
  const [delivery, setDelivery] = useState<DeliveryChoice>(DEFAULT_DELIVERY)
  const [until, setUntil] = useState<Until>('unsubscribed')
  const [errors, setErrors] = useState<SubscriptionErrors>({})
  const [pending, setPending] = useState(false)
  const [focusTick, setFocusTick] = useState(0)

  useEffect(() => {
    if (focusTick > 0)
      focusFirstInvalid(formRef.current)
  }, [focusTick])

  function check(): SubscriptionErrors {
    const found: SubscriptionErrors = {}
    if (device === '')
      found.device = 'Required.'
    const topicError = checkTopic(topic.trim())
    if (topicError)
      found.topic = topicError
    return found
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found = check()
    setErrors(found)
    if (Object.keys(found).length > 0) {
      setFocusTick(n => n + 1)
      return
    }
    setPending(true)
    onPendingChange(true)
    try {
      await onCreate({
        device,
        topic: topic.trim(),
        ...(events.length > 0 && {
          events: EVENTS.filter(e => events.includes(e)),
        }),
        ...(delivery !== DEFAULT_DELIVERY && { delivery }),
        until,
      })
      onCreated()
    }
    catch (error) {
      setErrors(subscriptionRefusal(error))
      setFocusTick(n => n + 1)
    }
    finally {
      setPending(false)
      onPendingChange(false)
    }
  }

  const deviceId = `${baseId}-device`
  const topicId = `${baseId}-topic`
  const eventsId = `${baseId}-events`
  const topicDescription = 'Choose one above, or type it: <module>:<instance>:<path…>, * as the last part for everything under it.'
  return (
    <>
      <DialogHeader>
        <DialogTitle>Follow a topic</DialogTitle>
        <DialogDescription>A speaker tells what happens in another system: a thread that stops, asks or fails.</DialogDescription>
      </DialogHeader>
      <form method="post" ref={formRef} noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex flex-col gap-5">
        <FormField id={deviceId} label="Speaker" required error={errors.device}>
          <NativeSelect
            id={deviceId}
            value={device}
            aria-required
            aria-invalid={errors.device ? true : undefined}
            aria-describedby={describedBy(deviceId, { error: errors.device })}
            onChange={(e) => {
              setDevice(e.target.value)
              setErrors(({ device: _, ...rest }) => rest)
            }}
          >
            {devices.length === 0 && <NativeSelectOption value="">No speaker is enrolled</NativeSelectOption>}
            {devices.map(d => <NativeSelectOption key={d.id} value={d.id}>{d.name}</NativeSelectOption>)}
          </NativeSelect>
        </FormField>
        <TopicPicker
          instances={instances}
          onChoose={(chosen) => {
            setTopic(chosen)
            setErrors(({ topic: _, ...rest }) => rest)
            topicRef.current?.focus()
          }}
        />
        <FormField id={topicId} label="Topic" required description={topicDescription} error={errors.topic}>
          <Input
            ref={topicRef}
            id={topicId}
            value={topic}
            className="font-mono text-sm"
            spellCheck={false}
            autoCapitalize="off"
            aria-required
            aria-invalid={errors.topic ? true : undefined}
            aria-describedby={describedBy(topicId, { description: true, error: errors.topic })}
            onChange={(e) => {
              setTopic(e.target.value)
              setErrors(({ topic: _, ...rest }) => rest)
            }}
          />
        </FormField>
        <fieldset
          aria-describedby={describedBy(eventsId, { description: true, error: errors.events })}
          data-invalid={errors.events ? 'true' : undefined}
          className="flex flex-col gap-2"
        >
          <legend className="text-sm font-medium">Events</legend>
          <p id={`${eventsId}-description`} className="text-sm text-muted-foreground">None chosen: every event the module sends.</p>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
            {EVENTS.map(name => (
              <Choice
                key={name}
                id={`${eventsId}-${name}`}
                checked={events.includes(name)}
                label={EVENT_TEXT[name]}
                onChange={(checked) => {
                  setEvents(list => checked
                    ? [...list, name]
                    : list.filter(e => e !== name))
                  setErrors(({ events: _, ...rest }) => rest)
                }}
              />
            ))}
          </div>
          <FieldMessage id={eventsId} error={errors.events} />
        </fieldset>
        <fieldset className="flex flex-col gap-2">
          <legend className="text-sm font-medium">Delivery</legend>
          {DELIVERY_CHOICES.map(choice => (
            <Choice
              key={choice.value}
              id={`${baseId}-delivery-${choice.value}`}
              type="radio"
              name={`${baseId}-delivery`}
              checked={delivery === choice.value}
              label={choice.label}
              description={choice.description}
              onChange={() => setDelivery(choice.value)}
            />
          ))}
        </fieldset>
        <fieldset className="flex flex-col gap-2">
          <legend className="text-sm font-medium">Lasts</legend>
          {UNTIL_CHOICES.map(choice => (
            <Choice
              key={choice.value}
              id={`${baseId}-until-${choice.value}`}
              type="radio"
              name={`${baseId}-until`}
              checked={until === choice.value}
              label={UNTIL_TEXT[choice.value]}
              description={choice.description}
              onChange={() => setUntil(choice.value)}
            />
          ))}
        </fieldset>
        {errors.form && <p role="alert" className="text-sm text-destructive">{errors.form}</p>}
        <DialogFooter>
          <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
          <Button type="submit" aria-disabled={pending || undefined}>
            {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
            Follow the topic
          </Button>
        </DialogFooter>
      </form>
    </>
  )
}
