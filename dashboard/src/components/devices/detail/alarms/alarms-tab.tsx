import type { Detail } from '@/api/gen'
import { AlarmClockIcon, TriangleAlertIcon } from 'lucide-react'
import { useId, useRef } from 'react'
import { EmptyState } from '@/components/data/states'
import { SettingsEditor } from '@/components/settings/settings-editor'
import { stampText } from '@/lib/format-time'
import { AddReminderDialog } from './add-reminder-dialog'
import { AddTimerDialog } from './add-timer-dialog'
import { AlarmsLists } from './alarms-lists'
import { kindText, parseAlarmsState, stoppedByText } from './alarms-state'
import { CancelReminderButton, CancelTimerButton } from './cancel-controls'
import { RingControls } from './ring-controls'
import { useAlarmsTimeZone } from './use-alarms-time-zone'

// alarms, timers and reminders on one tab: alarms are read-only (rule 7); a timer and a reminder without a level are
// the authenticated dashboard's sessionless commands (owner 2026-11-16)
export function AlarmsTab({ device }: { device: Detail }) {
  const capability = device.capabilities?.find(item => item.id === 'alarms')
  const { timeZone, configured } = useAlarmsTimeZone(device.id)
  const timerHeadingRef = useRef<HTMLHeadingElement>(null)
  const reminderHeadingRef = useRef<HTMLHeadingElement>(null)
  const settingsTitleId = useId()

  if (!capability) {
    return (
      <div className="rounded-lg border bg-card">
        <EmptyState icon={AlarmClockIcon} title="This speaker has no alarms capability" description="Its board does not report alarms, timers or reminders." />
      </div>
    )
  }

  const state = parseAlarmsState(capability.state)
  const last = state.lastRing
  return (
    <div className="flex flex-col gap-8">
      {state.ringing && <RingControls deviceId={device.id} ringing={state.ringing} />}
      {configured === false && (
        <div role="status" className="flex items-start gap-3 rounded-md border bg-warning-surface p-3">
          <TriangleAlertIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-warning" />
          <div className="flex flex-col gap-1">
            <p className="text-sm font-medium text-warning">Time zone is not set</p>
            <p className="text-sm">New alarms, timers and reminders need an explicit time zone. Set Time zone below, or in the global Settings.</p>
          </div>
        </div>
      )}
      <AlarmsLists
        state={state}
        timeZone={timeZone}
        addTimer={<AddTimerDialog deviceId={device.id} />}
        addReminder={<AddReminderDialog deviceId={device.id} />}
        cancelTimer={timer => (
          <CancelTimerButton deviceId={device.id} timer={timer} timeZone={timeZone} finalFocus={timerHeadingRef} />
        )}
        cancelReminder={reminder => (
          <CancelReminderButton deviceId={device.id} reminder={reminder} timeZone={timeZone} finalFocus={reminderHeadingRef} />
        )}
        timerHeadingRef={timerHeadingRef}
        reminderHeadingRef={reminderHeadingRef}
      />
      {last && (
        <section aria-label="Last ring" className="flex flex-col gap-1">
          <h2 className="text-lg font-semibold">Last ring</h2>
          <p className="text-sm text-muted-foreground">
            {`${kindText(last.kind)} at `}
            <time dateTime={last.at} className="font-semibold text-foreground">{stampText(last.at, timeZone)}</time>
            {`, ${stoppedByText(last.stoppedBy)}`}
          </p>
        </section>
      )}
      <section aria-labelledby={settingsTitleId} className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <h2 id={settingsTitleId} className="text-lg font-semibold">Alarm settings</h2>
          <p className="text-sm text-muted-foreground">Time zone and ring behavior. This device&apos;s overrides take precedence over the global values.</p>
        </div>
        <SettingsEditor scope={{ kind: 'device', deviceId: device.id }} keys={['timeZone', 'alarms']} groupTitle="Time zone and ringing" />
      </section>
    </div>
  )
}
