import type { FormEvent } from 'react'
import type { TimerErrors, TimerValues } from './alarm-form'
import { LoaderCircleIcon, PlusIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { describedBy } from '@/components/forms/fields/field-text'
import { focusFirstInvalid } from '@/components/forms/focus'
import { FormField } from '@/components/forms/form-field'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { timerArgs, validateTimer } from './alarm-form'
import { DurationFields } from './duration-fields'
import { useAlarmCommand } from './use-alarm-command'

const INITIAL: TimerValues = {
  id: '',
  duration: { hours: '0', minutes: '5', seconds: '0' },
}

function TimerForm({ deviceId, onDone, onPendingChange }: {
  deviceId: string
  onDone: () => void
  onPendingChange: (pending: boolean) => void
}) {
  const send = useAlarmCommand(deviceId)
  const baseId = useId()
  const formRef = useRef<HTMLFormElement>(null)
  const [values, setValues] = useState(INITIAL)
  const [errors, setErrors] = useState<TimerErrors>({})
  const [pending, setPending] = useState(false)
  const [focusTick, setFocusTick] = useState(0)

  useEffect(() => {
    if (focusTick > 0)
      focusFirstInvalid(formRef.current)
  }, [focusTick])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found = validateTimer(values)
    setErrors(found)
    if (Object.keys(found).length > 0) {
      setFocusTick(tick => tick + 1)
      return
    }
    setPending(true)
    onPendingChange(true)
    try {
      await send('set_timer', timerArgs(values))
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
  const durationId = `${baseId}-duration`
  return (
    <form method="post" ref={formRef} noValidate autoComplete="off" onSubmit={event => void submit(event)} className="flex flex-col gap-5">
      <FormField id={id} label="Timer ID" required mono description="1–16 lowercase letters or digits. Setting an existing ID replaces that timer." error={errors.id}>
        <Input
          id={id}
          value={values.id}
          aria-required
          aria-invalid={errors.id ? true : undefined}
          aria-describedby={describedBy(id, { description: true, error: errors.id })}
          onChange={(event) => {
            setValues(current => ({ ...current, id: event.target.value }))
            setErrors(({ id: _, form: __, ...rest }) => rest)
          }}
        />
      </FormField>
      <DurationFields
        id={durationId}
        values={values.duration}
        description="From 1 second to 24 hours."
        error={errors.duration}
        onChange={(duration) => {
          setValues(current => ({ ...current, duration }))
          setErrors(({ duration: _, form: __, ...rest }) => rest)
        }}
      />
      {errors.form && <p role="alert" className="text-sm text-destructive">{errors.form}</p>}
      <DialogFooter>
        <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
        <Button type="submit" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          Set timer
        </Button>
      </DialogFooter>
    </form>
  )
}

// the form goes as soon as its dialog starts closing; a request in flight holds the dialog open
export function AddTimerDialog({ deviceId }: { deviceId: string }) {
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
        Add timer
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Set a timer</DialogTitle>
          <DialogDescription>A timer rings at the speaker&apos;s current level and needs no woken voice session.</DialogDescription>
        </DialogHeader>
        <TimerForm key={opening} deviceId={deviceId} onDone={() => change(false)} onPendingChange={setPending} />
      </DialogContent>
    </Dialog>
  )
}
