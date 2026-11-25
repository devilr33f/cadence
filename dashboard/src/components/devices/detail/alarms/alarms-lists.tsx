import type { ReactNode, RefObject } from 'react'
import type { Alarm, AlarmsState, OtherKind, Reminder, Timer } from './alarms-state'
import { useId } from 'react'
import { useSecondsLeft } from '@/hooks/use-now'
import { durationText, stampText } from '@/lib/format-time'
import { kindText, repeatText, snoozeOf } from './alarms-state'

function Part({ title, description, action, headingRef, children }: {
  title: string
  description?: string
  action?: ReactNode
  headingRef?: RefObject<HTMLHeadingElement | null>
  children: ReactNode
}) {
  const titleId = useId()
  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="flex min-w-0 flex-col gap-1">
          <h2
            ref={headingRef}
            id={titleId}
            tabIndex={headingRef
              ? -1
              : undefined}
            className="text-lg font-semibold outline-none"
          >
            {title}
          </h2>
          {description && <p className="max-w-prose text-sm text-muted-foreground">{description}</p>}
        </div>
        {action && <div className="ml-auto">{action}</div>}
      </div>
      <ul className="flex flex-col divide-y rounded-lg border bg-card">{children}</ul>
    </section>
  )
}

// a row: its time bold down the left, what it is and its marks beside it, any action at the row's end
function Row({ time, children, action }: { time: ReactNode, children: ReactNode, action?: ReactNode }) {
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-sm">
      <span className="min-w-14 font-semibold tabular-nums">{time}</span>
      <div className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-3 gap-y-1">{children}</div>
      {action && <div className="ml-auto shrink-0">{action}</div>}
    </li>
  )
}

function Empty({ children }: { children: ReactNode }) {
  return <li className="px-3 py-4 text-sm text-muted-foreground">{children}</li>
}

function Muted({ children }: { children: ReactNode }) {
  return <span className="text-muted-foreground">{children}</span>
}

function SnoozeMark({ state, kind, id, timeZone }: { state: AlarmsState, kind: string, id: string, timeZone: string | null }) {
  const snooze = snoozeOf(state, kind, id)
  if (!snooze)
    return null
  return <span className="text-warning">{`Snoozed until ${stampText(snooze.until, timeZone)}`}</span>
}

function rampText(alarm: Alarm): string | undefined {
  const from = alarm.rampFrom === 'quiet'
    ? 'quiet'
    : 'the current level'
  if (alarm.rampSeconds !== undefined)
    return `Ramp ${alarm.rampSeconds} s from ${from}`
  return alarm.rampFrom
    ? `Ramp from ${from}`
    : undefined
}

function AlarmRow({ alarm, state, timeZone }: { alarm: Alarm, state: AlarmsState, timeZone: string | null }) {
  const ramp = rampText(alarm)
  return (
    <Row time={alarm.time}>
      <span>{repeatText(alarm.days, alarm.date)}</span>
      {alarm.level !== undefined && <Muted>{`Level ${alarm.level}`}</Muted>}
      {ramp && <Muted>{ramp}</Muted>}
      {alarm.limitSeconds !== undefined && <Muted>{`Ring limit ${alarm.limitSeconds} s`}</Muted>}
      {alarm.next && <Muted>{`Next ${stampText(alarm.next, timeZone)}`}</Muted>}
      <SnoozeMark state={state} kind="alarm" id={alarm.id} timeZone={timeZone} />
    </Row>
  )
}

function TimerRow({ timer, state, timeZone, action }: { timer: Timer, state: AlarmsState, timeZone: string | null, action?: ReactNode }) {
  const left = useSecondsLeft(Date.parse(timer.endsAt))
  return (
    <Row time={<span aria-label={`${left} seconds left`}>{durationText(left * 1000)}</span>} action={action}>
      <Muted>{`of ${durationText(timer.seconds * 1000)}, ends ${stampText(timer.endsAt, timeZone, true)}`}</Muted>
      <SnoozeMark state={state} kind="timer" id={timer.id} timeZone={timeZone} />
    </Row>
  )
}

function ReminderRow({ reminder, state, timeZone, action }: { reminder: Reminder, state: AlarmsState, timeZone: string | null, action?: ReactNode }) {
  return (
    <Row time={reminder.time ?? '--:--'} action={action}>
      <span className="min-w-0 break-words whitespace-pre-wrap">{reminder.label || 'No label'}</span>
      <Muted>{repeatText(reminder.days, reminder.date)}</Muted>
      {reminder.level !== undefined && <Muted>{`Level ${reminder.level}`}</Muted>}
      {reminder.at
        ? <Muted>{`At ${stampText(reminder.at, timeZone)}`}</Muted>
        : reminder.next && <Muted>{`Next ${stampText(reminder.next, timeZone)}`}</Muted>}
      <SnoozeMark state={state} kind="reminder" id={reminder.id} timeZone={timeZone} />
    </Row>
  )
}

function OtherPart({ other, timeZone }: { other: OtherKind, timeZone: string | null }) {
  return (
    <Part title={`${kindText(other.kind)}s`}>
      {other.items.map(item => (
        <Row
          key={item.id}
          time={item.next
            ? stampText(item.next, timeZone)
            : '--:--'}
        >
          <span className="min-w-0 break-words whitespace-pre-wrap">{item.label ?? item.id}</span>
        </Row>
      ))}
    </Part>
  )
}

// the full schedule on its own tab: alarms stay read-only (rule 7); timer and reminder actions are supplied by the tab
export function AlarmsLists({ state, timeZone, addTimer, addReminder, cancelTimer, cancelReminder, timerHeadingRef, reminderHeadingRef }: {
  state: AlarmsState
  timeZone: string | null
  addTimer?: ReactNode
  addReminder?: ReactNode
  cancelTimer?: (timer: Timer) => ReactNode
  cancelReminder?: (reminder: Reminder) => ReactNode
  timerHeadingRef?: RefObject<HTMLHeadingElement | null>
  reminderHeadingRef?: RefObject<HTMLHeadingElement | null>
}) {
  return (
    <>
      <Part title="Alarms" description="Read-only here: alarms are set and cancelled only in a woken voice session.">
        {state.alarms.length === 0
          ? <Empty>No alarms are set. Ask the speaker during a woken voice session to set one.</Empty>
          : state.alarms.map(alarm => <AlarmRow key={alarm.id} alarm={alarm} state={state} timeZone={timeZone} />)}
      </Part>
      <Part title="Timers" action={addTimer} headingRef={timerHeadingRef}>
        {state.timers.length === 0
          ? <Empty>No timers are running.</Empty>
          : state.timers.map(timer => (
              <TimerRow key={timer.id} timer={timer} state={state} timeZone={timeZone} action={cancelTimer?.(timer)} />
            ))}
      </Part>
      <Part title="Reminders" action={addReminder} headingRef={reminderHeadingRef}>
        {state.reminders.length === 0
          ? <Empty>No reminders are set.</Empty>
          : state.reminders.map(reminder => (
              <ReminderRow key={reminder.id} reminder={reminder} state={state} timeZone={timeZone} action={cancelReminder?.(reminder)} />
            ))}
      </Part>
      {state.others.map(other => <OtherPart key={other.kind} other={other} timeZone={timeZone} />)}
    </>
  )
}
