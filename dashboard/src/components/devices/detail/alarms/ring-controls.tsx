import type { Ref } from './alarms-state'
import { useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { StatusBadge } from '@/components/data/status-badge'
import { Button } from '@/components/ui/button'
import { kindText } from './alarms-state'
import { useAlarmCommand } from './use-alarm-command'

type RingCommand = 'stop_ring' | 'snooze'

const FAILED: Record<RingCommand, string> = {
  stop_ring: 'Stop',
  snooze: 'Snooze',
}

// a ring in progress, and the two presses that need no session: stop it, or put it off by the device's snooze minutes.
// the dashboard never sets or cancels an alarm (rule 7)
export function RingControls({ deviceId, ringing }: { deviceId: string, ringing: Ref }) {
  const sendCommand = useAlarmCommand(deviceId)
  const [running, setRunning] = useState<RingCommand>()
  const [failure, setFailure] = useState<string>()
  // the state lags a render behind: two presses in one frame both read it unset
  const busyRef = useRef(false)

  async function send(name: RingCommand) {
    if (busyRef.current)
      return
    busyRef.current = true
    setRunning(name)
    setFailure(undefined)
    try {
      await sendCommand(name, { kind: ringing.kind, id: ringing.id })
    }
    catch (error) {
      setFailure(`${FAILED[name]} failed. ${describeError(error)}`)
    }
    finally {
      busyRef.current = false
      setRunning(undefined)
    }
  }

  return (
    <div className="flex w-full flex-col gap-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border bg-warning-surface p-3">
        <StatusBadge tone="warning" label="Ringing" detail={`${kindText(ringing.kind)} ${ringing.id}`} />
        <div className="ml-auto flex gap-2">
          <Button type="button" size="sm" variant="secondary" aria-disabled={running !== undefined} onClick={() => void send('stop_ring')}>
            Stop
          </Button>
          <Button type="button" size="sm" variant="outline" aria-disabled={running !== undefined} onClick={() => void send('snooze')}>
            Snooze
          </Button>
        </div>
      </div>
      {failure && <p role="alert" className="text-xs text-destructive">{failure}</p>}
    </div>
  )
}
