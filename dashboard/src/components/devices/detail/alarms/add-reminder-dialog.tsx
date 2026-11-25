import type { FormEvent } from 'react'
import type { ReminderErrors, ReminderValues } from './alarm-form'
import { LoaderCircleIcon, PlusIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { Choice } from '@/components/forms/choice'
import { FieldMessage, RequiredMark } from '@/components/forms/fields/field-shell'
import { describedBy } from '@/components/forms/fields/field-text'
import { focusFirstInvalid } from '@/components/forms/focus'
import { FormField } from '@/components/forms/form-field'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { reminderArgs, validateReminder, WEEKDAYS } from './alarm-form'
import { DurationFields } from './duration-fields'
import { useAlarmCommand } from './use-alarm-command'

const INITIAL: ReminderValues = {
  id: '',
  label: '',
  mode: 'after',
  duration: { hours: '0', minutes: '10', seconds: '0' },
  time: '18:00',
  repeat: 'once',
  date: '',
  days: [],
}

function ReminderForm({ deviceId, onDone, onPendingChange }: {
  deviceId: string
  onDone: () => void
  onPendingChange: (pending: boolean) => void
}) {
  const send = useAlarmCommand(deviceId)
  const baseId = useId()
  const formRef = useRef<HTMLFormElement>(null)
  const [values, setValues] = useState(INITIAL)
  const [errors, setErrors] = useState<ReminderErrors>({})
  const [pending, setPending] = useState(false)
  const [focusTick, setFocusTick] = useState(0)

  useEffect(() => {
    if (focusTick > 0)
      focusFirstInvalid(formRef.current)
  }, [focusTick])

  function change(next: Partial<ReminderValues>, ...cleared: (keyof ReminderErrors)[]) {
    setValues(current => ({
      ...current,
      ...next,
    }))
    setErrors((current) => {
      const rest = { ...current }
      delete rest.form
      for (const key of cleared)
        delete rest[key]
      return rest
    })
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found = validateReminder(values)
    setErrors(found)
    if (Object.keys(found).length > 0) {
      setFocusTick(tick => tick + 1)
      return
    }
    setPending(true)
    onPendingChange(true)
    try {
      await send('set_reminder', reminderArgs(values))
      onDone()
    }
    catch (error) {
      setErrors({
        form: describeError(error),
      })
    }
    finally {
      setPending(false)
      onPendingChange(false)
    }
  }

  const id = `${baseId}-id`
  const labelId = `${baseId}-label`
  const durationId = `${baseId}-duration`
  const timeId = `${baseId}-time`
  const repeatId = `${baseId}-repeat`
  const dateId = `${baseId}-date`
  const daysId = `${baseId}-days`
  return (
    <form method="post" ref={formRef} noValidate autoComplete="off" onSubmit={event => void submit(event)} className="flex flex-col gap-5">
      <FormField id={id} label="Reminder ID" required mono description="1–16 lowercase letters or digits. Setting an existing ID replaces that reminder." error={errors.id}>
        <Input
          id={id}
          value={values.id}
          aria-required
          aria-invalid={errors.id ? true : undefined}
          aria-describedby={describedBy(id, { description: true, error: errors.id })}
          onChange={event => change({ id: event.target.value }, 'id')}
        />
      </FormField>
      <FormField id={labelId} label="Text" required description="Spoken when it rings; its start is shown as the label. At most 500 characters." error={errors.label}>
        <Input
          id={labelId}
          value={values.label}
          aria-required
          aria-invalid={errors.label ? true : undefined}
          aria-describedby={describedBy(labelId, { description: true, error: errors.label })}
          onChange={event => change({ label: event.target.value }, 'label')}
        />
      </FormField>
      <fieldset className="flex flex-col gap-3">
        <legend className="text-sm font-medium">
          When
          <RequiredMark required />
        </legend>
        <div className="grid gap-3 sm:grid-cols-2">
          <Choice
            id={`${baseId}-after`}
            type="radio"
            name={`${baseId}-mode`}
            checked={values.mode === 'after'}
            onChange={() => change({ mode: 'after' }, 'duration', 'time', 'date', 'days')}
            label="After a delay"
          />
          <Choice
            id={`${baseId}-clock`}
            type="radio"
            name={`${baseId}-mode`}
            checked={values.mode === 'clock'}
            onChange={() => change({ mode: 'clock' }, 'duration', 'time', 'date', 'days')}
            label="At a time"
          />
        </div>
      </fieldset>
      {values.mode === 'after'
        ? (
            <DurationFields
              id={durationId}
              values={values.duration}
              description="From 1 second to 7 days from now."
              error={errors.duration}
              onChange={duration => change({ duration }, 'duration')}
            />
          )
        : (
            <>
              <FormField id={timeId} label="Time" required error={errors.time}>
                <Input
                  id={timeId}
                  type="time"
                  value={values.time}
                  aria-required
                  aria-invalid={errors.time ? true : undefined}
                  aria-describedby={describedBy(timeId, { error: errors.time })}
                  onChange={event => change({ time: event.target.value }, 'time')}
                />
              </FormField>
              <FormField id={repeatId} label="Repeats" required>
                <NativeSelect
                  id={repeatId}
                  className="w-full"
                  value={values.repeat}
                  aria-required
                  onChange={event => change({ repeat: event.target.value as ReminderValues['repeat'] }, 'date', 'days')}
                >
                  <NativeSelectOption value="once">Once, at the next occurrence</NativeSelectOption>
                  <NativeSelectOption value="date">Once, on a chosen date</NativeSelectOption>
                  <NativeSelectOption value="days">Every selected weekday</NativeSelectOption>
                </NativeSelect>
              </FormField>
              {values.repeat === 'date' && (
                <FormField id={dateId} label="Date" required error={errors.date}>
                  <Input
                    id={dateId}
                    type="date"
                    value={values.date}
                    aria-required
                    aria-invalid={errors.date ? true : undefined}
                    aria-describedby={describedBy(dateId, { error: errors.date })}
                    onChange={event => change({ date: event.target.value }, 'date')}
                  />
                </FormField>
              )}
              {values.repeat === 'days' && (
                <fieldset
                  className="flex flex-col gap-3"
                  aria-invalid={errors.days ? true : undefined}
                  aria-describedby={describedBy(daysId, { error: errors.days })}
                >
                  <legend className="text-sm font-medium">
                    Repeat on
                    <RequiredMark required />
                  </legend>
                  <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
                    {WEEKDAYS.map(day => (
                      <Choice
                        key={day.id}
                        id={`${daysId}-${day.id}`}
                        checked={values.days.includes(day.id)}
                        onChange={checked => change({
                          days: checked
                            ? [...values.days, day.id]
                            : values.days.filter(value => value !== day.id),
                        }, 'days')}
                        label={day.label}
                      />
                    ))}
                  </div>
                  <FieldMessage id={daysId} error={errors.days} />
                </fieldset>
              )}
            </>
          )}
      {errors.form && <p role="alert" className="text-sm text-destructive">{errors.form}</p>}
      <DialogFooter>
        <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
        <Button type="submit" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          Set reminder
        </Button>
      </DialogFooter>
    </form>
  )
}

export function AddReminderDialog({ deviceId }: { deviceId: string }) {
  const [open, setOpen] = useState(false)
  const [pending, setPending] = useState(false)
  const [opening, setOpening] = useState(0)
  const change = (next: boolean) => {
    if (!next)
      setOpening(value => value + 1)
    setOpen(next)
  }
  return (
    <Dialog open={open} onOpenChange={next => !pending && change(next)}>
      <DialogTrigger render={<Button type="button" variant="outline" size="sm" />}>
        <PlusIcon aria-hidden="true" />
        Add reminder
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Set a reminder</DialogTitle>
          <DialogDescription>No level is set here: the reminder rings at the speaker&apos;s current level.</DialogDescription>
        </DialogHeader>
        <ReminderForm key={opening} deviceId={deviceId} onDone={() => change(false)} onPendingChange={setPending} />
      </DialogContent>
    </Dialog>
  )
}
