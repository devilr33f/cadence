import type { BusySignal } from '@/api/gen'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { StatusBadge } from '@/components/data/status-badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { DND_DURATIONS, DND_SOURCE, useBusyActions } from '@/lib/busy'
import { stampText } from '@/lib/format-time'
import { useTimeZone } from '@/lib/time-zone'

// the owner's do-not-disturb from the dashboard: every speaker, for a chosen time (source dashboard). ending it ends
// this source alone: a call or another source keeps the speakers busy
export function DndControl({ signals }: { signals: readonly BusySignal[] | undefined }) {
  const selectId = useId()
  const timeZone = useTimeZone()
  const actions = useBusyActions()
  const [minutes, setMinutes] = useState(DND_DURATIONS[1]!.minutes)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const dnd = signals?.find(signal => signal.source === DND_SOURCE)
  const on = dnd !== undefined
  // the pressed button goes once the read shows the change: the focus moves to the control that took its place
  const handOverRef = useRef(false)
  const endRef = useRef<HTMLButtonElement>(null)
  const startRef = useRef<HTMLSelectElement>(null)
  useEffect(() => {
    if (!handOverRef.current)
      return
    handOverRef.current = false
    if (on)
      endRef.current?.focus()
    else
      startRef.current?.focus()
  }, [on])

  async function run(action: () => Promise<unknown>) {
    if (pending)
      return
    setPending(true)
    setError(undefined)
    try {
      await action()
      handOverRef.current = true
    }
    catch (failure) {
      setError(describeError(failure))
    }
    finally {
      setPending(false)
    }
  }

  const spinner = pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-end gap-3">
        {dnd
          ? (
              <>
                <p role="status" className="flex flex-wrap items-center gap-2 text-base">
                  <StatusBadge tone="warning" label="Do not disturb" appearance="pill" />
                  {dnd.expiresAt && <span>{`Until ${stampText(dnd.expiresAt, timeZone)}`}</span>}
                </p>
                <Button ref={endRef} type="button" variant="outline" aria-disabled={pending || undefined} onClick={() => void run(() => actions.clear(DND_SOURCE))}>
                  {spinner}
                  End do not disturb
                </Button>
              </>
            )
          : (
              <>
                <div className="flex flex-col gap-2">
                  <Label htmlFor={selectId}>Do not disturb for</Label>
                  <NativeSelect ref={startRef} id={selectId} value={String(minutes)} onChange={e => setMinutes(Number(e.target.value))}>
                    {DND_DURATIONS.map(duration => (
                      <NativeSelectOption key={duration.minutes} value={String(duration.minutes)}>{duration.label}</NativeSelectOption>
                    ))}
                  </NativeSelect>
                </div>
                <Button type="button" variant="secondary" aria-disabled={pending || undefined} onClick={() => void run(() => actions.startDnd(minutes))}>
                  {spinner}
                  Turn on do not disturb
                </Button>
              </>
            )}
      </div>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </div>
  )
}
