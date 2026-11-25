import type { NextUp } from './alarms-state'
import type { CapabilityView } from '@/api/gen'
import { ArrowRightIcon } from 'lucide-react'
import { useId } from 'react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { stampText } from '@/lib/format-time'
import { CapabilityBadge } from '../capability-badge'
import { kindText, nextUp, parseAlarmsState, stoppedByText } from './alarms-state'
import { RingControls } from './ring-controls'
import { useAlarmsTimeZone } from './use-alarms-time-zone'

function Next({ next, timeZone }: { next: NextUp | undefined, timeZone: string | null }) {
  if (!next)
    return <p className="text-sm text-muted-foreground">Nothing is set.</p>
  return (
    <div className="flex flex-col">
      <span className="text-xs font-medium text-muted-foreground">{`Next: ${kindText(next.kind)}`}</span>
      <span className="text-2xl font-semibold tabular-nums">{stampText(next.at, timeZone)}</span>
      {next.label && <span className="text-sm break-words whitespace-pre-wrap">{next.label}</span>}
    </div>
  )
}

// the overview's compact summary; the tab has the full schedule, the permitted commands and the settings
export function AlarmsCard({ deviceId, capability, onOpen }: { deviceId: string, capability: CapabilityView, onOpen: () => void }) {
  const titleId = useId()
  const { timeZone } = useAlarmsTimeZone(deviceId)
  const state = parseAlarmsState(capability.state)
  const last = state.lastRing
  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 id={titleId} className="font-mono text-sm font-medium">{capability.id}</h3>
        <span className="text-sm text-muted-foreground">{`v${capability.version}`}</span>
        <CapabilityBadge capability={capability} />
      </CardHeader>
      <CardContent className="flex flex-col items-start gap-4">
        {state.ringing && <RingControls deviceId={deviceId} ringing={state.ringing} />}
        <Next next={nextUp(state)} timeZone={timeZone} />
        {last && (
          <p className="text-sm text-muted-foreground">
            {`Last ring: ${kindText(last.kind)} at `}
            <time dateTime={last.at} className="font-semibold text-foreground">{stampText(last.at, timeZone)}</time>
            {`, ${stoppedByText(last.stoppedBy)}`}
          </p>
        )}
        <Button type="button" variant="outline" size="sm" onClick={onOpen}>
          Open Alarms
          <ArrowRightIcon aria-hidden="true" />
        </Button>
      </CardContent>
    </Card>
  )
}
