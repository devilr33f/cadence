import type { RefObject } from 'react'
import type { Reminder, Timer } from './alarms-state'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { Button } from '@/components/ui/button'
import { durationText, stampText } from '@/lib/format-time'
import { repeatText } from './alarms-state'
import { useAlarmCommand } from './use-alarm-command'

export function CancelTimerButton({ deviceId, timer, timeZone, finalFocus }: {
  deviceId: string
  timer: Timer
  timeZone: string | null
  finalFocus: RefObject<HTMLElement | null>
}) {
  const send = useAlarmCommand(deviceId)
  return (
    <ConfirmDialog
      trigger={<Button type="button" variant="ghost" size="sm" className="text-destructive" aria-label={`Cancel timer ${timer.id}`}>Cancel</Button>}
      title="Cancel this timer?"
      description={(
        <span className="flex flex-col gap-1.5">
          <span>{`Its ${durationText(timer.seconds * 1000)} countdown ends ${stampText(timer.endsAt, timeZone, true)}.`}</span>
          <code className="font-mono text-xs">{timer.id}</code>
        </span>
      )}
      confirmLabel="Cancel timer"
      tone="destructive"
      inlineError
      finalFocus={finalFocus}
      onConfirm={() => send('cancel_timer', { id: timer.id })}
    />
  )
}

export function CancelReminderButton({ deviceId, reminder, timeZone, finalFocus }: {
  deviceId: string
  reminder: Reminder
  timeZone: string | null
  finalFocus: RefObject<HTMLElement | null>
}) {
  const send = useAlarmCommand(deviceId)
  const name = reminder.label || reminder.id
  const at = reminder.at ?? reminder.next
  return (
    <ConfirmDialog
      trigger={<Button type="button" variant="ghost" size="sm" className="text-destructive" aria-label={`Cancel reminder: ${name}`}>Cancel</Button>}
      title="Cancel this reminder?"
      description={(
        <span className="flex flex-col gap-1.5">
          <span className="break-words whitespace-pre-wrap">{name}</span>
          <span>
            {repeatText(reminder.days, reminder.date)}
            {at
              ? ` · ${stampText(at, timeZone)}`
              : ''}
          </span>
          <code className="font-mono text-xs">{reminder.id}</code>
        </span>
      )}
      confirmLabel="Cancel reminder"
      tone="destructive"
      inlineError
      finalFocus={finalFocus}
      onConfirm={() => send('cancel_reminder', { id: reminder.id })}
    />
  )
}
