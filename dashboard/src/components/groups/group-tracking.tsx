import type { GroupMemberView, GroupRequest, GroupView, TrackingMemberView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import type { Tone } from '@/components/data/tone'
import { useId, useState } from 'react'
import { ApiError } from '@/api/error'
import { DataTable } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/status-badge'
import { Switch } from '@/components/ui/switch'
import { draftOf, requestOf } from './group-draft'

const HELP = 'While music plays, an inaudible 18–20 kHz signal joins it and the speakers\' microphones listen; only '
  + 'timing numbers are kept. It needs a calibration that showed ultrasound reaches each member, and Bluetooth outputs '
  + 'cannot carry it.'

const ULTRASONIC = {
  ok: 'Reached (ok)',
  weak: 'Weak (weak)',
  missed: 'Not heard (missed)',
  unsupported: 'Unsupported',
} as const

const STATES: Readonly<Record<string, { label: string, tone: Tone }>> = {
  off: { label: 'Off', tone: 'neutral' },
  idle: { label: 'Idle', tone: 'neutral' },
  needs_calibration: { label: 'Needs calibration', tone: 'warning' },
  unsupported: { label: 'Unsupported', tone: 'neutral' },
}

interface Row {
  member: GroupMemberView
  name: string
  ultrasonic?: keyof typeof ULTRASONIC
  tracked?: TrackingMemberView
}

function milliseconds(us: number, signed = false): string {
  const sign = signed && us >= 0
    ? '+'
    : ''
  return `${sign}${(us / 1000).toFixed(2)} ms`
}

function correctionAge(at: string): string {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(at).getTime()) / 1000))
  if (seconds < 60)
    return `${seconds} s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60)
    return `${minutes} min ago`
  return `${Math.floor(minutes / 60)} h ago`
}

const NONE = <span className="text-muted-foreground">—</span>

const COLUMNS: readonly Column<Row>[] = [
  {
    id: 'device',
    header: 'Device',
    cell: r => <span className="font-medium">{r.name}</span>,
  },
  {
    id: 'ultrasonic',
    header: 'Ultrasonic',
    cell: r => r.ultrasonic
      ? <span>{ULTRASONIC[r.ultrasonic]}</span>
      : NONE,
  },
  {
    id: 'offset',
    header: 'Offset ± 95%',
    align: 'end',
    cell: r => r.tracked
      ? <span className="tabular-nums">{`${milliseconds(r.tracked.offsetUs, true)} ± ${milliseconds(r.tracked.ci95Us)}`}</span>
      : NONE,
  },
  {
    id: 'correction',
    header: 'Last correction',
    align: 'end',
    cell: r => r.tracked?.lastCorrectionUs !== undefined && r.tracked.lastCorrectionAt
      ? <span className="tabular-nums">{`${milliseconds(r.tracked.lastCorrectionUs)} · ${correctionAge(r.tracked.lastCorrectionAt)}`}</span>
      : NONE,
  },
]

function updateRefusal(error: unknown): string {
  return error instanceof ApiError
    ? `The server answered ${error.status} ${error.error}.`
    : 'The update could not be sent.'
}

// ultrasonic tracking is an immediate group setting; the table also keeps the last calibration's capability visible
// while the switch is off. microphone use has no led indication: this dashboard state is its only indication
export function GroupTracking({ group, names, onSave }: {
  group: GroupView
  names: ReadonlyMap<string, string>
  onSave: (id: string, body: GroupRequest) => Promise<unknown>
}) {
  const titleId = useId()
  const helpId = useId()
  const [choice, setChoice] = useState<{ from: boolean, value: boolean }>()
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const enabled = choice && group.trackingEnabled === choice.from
    ? choice.value
    : group.trackingEnabled
  const tracking = group.tracking ?? { state: 'off', members: [] }
  const listening = enabled && tracking.state === 'tracking'
  const state = STATES[tracking.state]

  async function change(value: boolean) {
    if (pending)
      return
    setChoice({ from: group.trackingEnabled, value })
    setPending(true)
    setError(undefined)
    try {
      await onSave(group.id, {
        ...requestOf(draftOf(group)),
        trackingEnabled: value,
      })
    }
    catch (failure) {
      setChoice(undefined)
      setError(updateRefusal(failure))
    }
    finally {
      setPending(false)
    }
  }

  const rows = group.members.map((member): Row => {
    const output = member.status?.output?.id
    const ultrasonic = member.trims?.find(trim => trim.outputId === output)?.ultrasonic
    return {
      member,
      name: names.get(member.deviceId) ?? member.deviceId,
      ultrasonic,
      tracked: enabled
        ? tracking.members.find(m => m.deviceId === member.deviceId)
        : undefined,
    }
  })

  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <h2 id={titleId} className="text-lg font-semibold">Live tracking (ultrasonic)</h2>
        <Switch
          aria-labelledby={titleId}
          aria-describedby={helpId}
          aria-disabled={pending || undefined}
          checked={enabled}
          onCheckedChange={value => void change(value)}
        />
        {listening
          ? <StatusBadge tone="info" label="Microphones listening" appearance="pill" />
          : state && enabled && <StatusBadge tone={state.tone} label={state.label} />}
      </div>
      <p id={helpId} className="max-w-[65ch] text-sm text-muted-foreground">{HELP}</p>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      <DataTable
        caption="Live tracking members"
        columns={COLUMNS}
        rows={rows}
        rowKey={r => r.member.deviceId}
        empty={<p className="p-4 text-sm text-muted-foreground">No members.</p>}
      />
    </section>
  )
}
