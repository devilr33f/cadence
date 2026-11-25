import type { CalibrationMemberView, CalibrationView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import type { Tone } from '@/components/data/tone'
import type { ListenerPhase } from '@/lib/listener-calibration'
import { Loader2Icon, Volume2Icon } from 'lucide-react'
import { useId, useState } from 'react'
import { DataTable } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/status-badge'
import { Button } from '@/components/ui/button'
import { failureText, needsBoost, nextBoosts, PHASES } from './calibration-retry'
import { outputKind } from './group-words'

// what the confirm says before anything sounds: the owner starts it, knowing every speaker chirps (rule 7)
export const CALIBRATE_DESCRIPTION = 'Every speaker in the group plays a short chirp a few times, quieter than its own '
  + 'volume, while every microphone listens. Only the new trims and whether ultrasound reached a microphone are kept. '
  + 'Pause the group\'s music first; it takes up to half a minute.'

// a calibration's codes (server/internal/groups, pkg/capschema): ok applies the trims, the rest change nothing
const RESULTS: Readonly<Record<string, { label: string, tone: Tone, text: string }>> = {
  ok: { label: 'Applied', tone: 'success', text: 'Every speaker was measured and its trim set.' },
  incomplete: {
    label: 'Not applied',
    tone: 'warning',
    text: 'Some speaker could not be measured (see its result); no trim changed.',
  },
  inconsistent: {
    label: 'Not applied',
    tone: 'warning',
    text: 'The measures disagree by more than a millisecond (an echo, a speaker moved); no trim changed. Try again.',
  },
  out_of_range: {
    label: 'Not applied',
    tone: 'warning',
    text: 'A speaker would need more than half a second of trim (a chirp taken for another?); no trim changed. Try again.',
  },
  expired: {
    label: 'Not applied',
    tone: 'warning',
    text: 'The leader no longer holds this run; no trim changed. Try again.',
  },
}

const MODES: Readonly<Record<string, string>> = {
  listener: 'Listener',
  mics: 'Speaker microphones',
}

// a member's result: ok, or why it could not be measured
const MEMBER_CODES: Readonly<Record<string, string>> = {
  ok: 'Measured',
  no_self: 'Did not hear its own chirp',
  isolated: 'No other microphone heard it',
  unstable: 'Rounds disagree',
  no_answer: 'Did not answer',
  analysis_timeout: 'Analysis took too long',
  offline: 'Offline',
  busy: 'Playing something else',
  gated: 'Volume not set yet',
  no_capture: 'No microphone',
  muted: 'Microphone muted',
  unsynced: 'Clock not synced',
  epoch: 'Playing other audio',
  weak: 'Heard too faintly',
  missed: 'Not heard: its probe was not found',
  scheduled: 'Not measured yet',
}

// the speakers' codes for a chirp a device did not play (its own refusal of the run)
const PLAY_REFUSALS: ReadonlySet<string> = new Set([
  'busy',
  'gated',
  'unsynced',
  'epoch',
  'offline',
])

// sound's speed, for the path's metres
const SOUND_M_PER_S = 343

interface Row {
  member: CalibrationMemberView
  name: string
  // the run's rounds, 0 when it did not say
  total: number
}

function ms(us: number): string {
  return `${(us / 1000).toFixed(2)} ms`
}

const NONE = <span className="text-muted-foreground">—</span>

function measured(m: CalibrationMemberView): boolean {
  return m.code === 'ok' || m.code === 'unstable'
}

const COLUMNS: readonly Column<Row>[] = [
  {
    id: 'device',
    header: 'Device',
    cell: r => (
      <span className="flex flex-col">
        <span className="font-medium">{r.name}</span>
        {r.member.output && (
          <span className="text-xs text-muted-foreground">
            {outputKind(r.member.output.kind)}
            {r.member.output.id !== r.member.output.kind && ` · ${r.member.output.id}`}
          </span>
        )}
        {r.member.micChannels > 0 && (
          <span className="text-xs text-muted-foreground tabular-nums">
            {`${r.member.micChannels} ${r.member.micChannels === 1
              ? 'mic'
              : 'mics'}`}
          </span>
        )}
      </span>
    ),
  },
  {
    id: 'result',
    header: 'Result',
    cell: (r) => {
      if (r.member.code === 'ok')
        return <StatusBadge tone="success" label="Measured" />
      // a phone run's chirp the device declined to play: its reason, not just that nothing was heard
      const refused = r.member.micsCode && PLAY_REFUSALS.has(r.member.micsCode) && !measured(r.member)
      return refused
        ? <StatusBadge tone="warning" label={`Did not play: ${MEMBER_CODES[r.member.micsCode!]} (${r.member.micsCode})`} />
        : <StatusBadge tone="warning" label={`${MEMBER_CODES[r.member.code] ?? 'Not measured'} (${r.member.code})`} />
    },
  },
  {
    id: 'offset',
    header: 'Latency vs. the others',
    align: 'end',
    cell: r => r.member.code === 'ok'
      ? <span className="tabular-nums">{ms(r.member.offsetUs)}</span>
      : NONE,
  },
  {
    id: 'spread',
    header: 'Spread',
    align: 'end',
    cell: r => measured(r.member) && r.member.rounds > 0
      ? <span className="tabular-nums">{ms(r.member.spreadUs)}</span>
      : NONE,
    className: 'max-md:hidden',
  },
  {
    id: 'ci95',
    header: '95% range',
    align: 'end',
    cell: r => measured(r.member) && r.member.ci95Us > 0
      ? <span className="tabular-nums">{`±${ms(r.member.ci95Us)}`}</span>
      : NONE,
    className: 'max-md:hidden',
  },
  {
    id: 'rounds',
    header: 'Rounds kept',
    align: 'end',
    cell: r => r.member.rounds > 0
      ? (
          <span className="tabular-nums">
            {r.total > 0
              ? `${r.member.rounds}/${r.total}`
              : `${r.member.rounds}`}
          </span>
        )
      : NONE,
    className: 'max-md:hidden',
  },
  {
    id: 'trim',
    header: 'Trim now',
    align: 'end',
    cell: r => <span className="tabular-nums">{ms(r.member.trimUs)}</span>,
  },
  {
    id: 'confidence',
    header: 'Confidence',
    align: 'end',
    cell: r => measured(r.member)
      ? <span className="tabular-nums">{`${Math.round(r.member.confidence * 100)}%`}</span>
      : NONE,
    className: 'max-md:hidden',
  },
  {
    id: 'ultrasonic',
    header: 'Ultrasonic',
    cell: (r) => {
      const labels = {
        ok: 'Reached (ok)',
        weak: 'Weak (weak)',
        missed: 'Not heard (missed)',
        unsupported: 'Unsupported',
      } as const
      return r.member.ultrasonic
        ? labels[r.member.ultrasonic]
        : NONE
    },
  },
]

// a listener run the speakers measured too: their own offsets, and each member's path to the listener against the
// others' (informational: nothing of it applies)
const MICS_COLUMNS: readonly Column<Row>[] = [
  {
    id: 'mics',
    header: 'Speaker mics',
    align: 'end',
    cell: (r) => {
      const code = r.member.micsCode
      if (!code)
        return NONE
      return code === 'ok'
        ? <span className="tabular-nums">{ms(r.member.micsOffsetUs ?? 0)}</span>
        : <span className="text-muted-foreground">{MEMBER_CODES[code] ?? code}</span>
    },
  },
  {
    id: 'path',
    header: 'Path to you',
    align: 'end',
    cell: r => r.member.pathUs === undefined
      ? NONE
      : <span className="tabular-nums">{`${ms(r.member.pathUs)} · ≈${(r.member.pathUs / 1e6 * SOUND_M_PER_S).toFixed(2)} m`}</span>,
  },
]

// speaker microphones need every pair of members to hear each other: a member with no microphone (a tv stick) makes
// every run incomplete, so the panel says which mode can work
export const NO_MIC_HINT = 'A speaker without a microphone cannot be measured this way. Calibrate with this device\'s '
  + 'microphone instead, standing where you listen.'

export const BOOST_NOTE = 'Plays only these speakers\' chirps louder, by 6 dB a try: still quieter than their own '
  + 'volume, which never changes.'

export const BOOST_MAXED = 'These speakers already chirp as loud as a calibration may (14 dB more); move closer, or '
  + 'make the room quieter, and calibrate again.'

// the last calibration this page ran: kept on the page alone, as the server keeps only its trims and ultrasonic
// capability results. a member heard faintly can be measured again with its chirps louder (onRetry, the owner's press:
// a listener run's microphone starts inside it)
export function GroupCalibration({ calibration, names, boosts = {}, onRetry }: {
  calibration: CalibrationView
  names: ReadonlyMap<string, string>
  boosts?: Readonly<Record<string, number>>
  onRetry?: (boosts: Record<string, number>, onPhase: (phase: ListenerPhase) => void) => Promise<void>
}) {
  const titleId = useId()
  const noteId = useId()
  const [pending, setPending] = useState(false)
  const [phase, setPhase] = useState<ListenerPhase>()
  const [error, setError] = useState<string>()
  const result = RESULTS[calibration.code] ?? { label: 'Not applied', tone: 'warning' as Tone, text: `No trim changed (${calibration.code}).` }
  const total = calibration.rounds ?? 0
  const withMics = calibration.members.some(m => m.micsCode)
  const faint = calibration.members.filter(needsBoost)
  const next = nextBoosts(calibration, boosts)
  const nameOf = (id: string) => names.get(id) ?? id

  function retry() {
    if (pending || !next || !onRetry)
      return
    setPending(true)
    setError(undefined)
    // called at once, inside the press: a listener run's microphone must start in it
    onRetry(next, setPhase)
      .catch((failure: unknown) => setError(failureText(failure)))
      .finally(() => {
        setPending(false)
        setPhase(undefined)
      })
  }

  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <h2 id={titleId} className="text-lg font-semibold">Calibration</h2>
        <StatusBadge tone={result.tone} label={result.label} />
        <span className="text-sm text-muted-foreground">{MODES[calibration.mode] ?? calibration.mode}</span>
      </div>
      <p role="status" className="text-sm text-muted-foreground">
        {result.text}
        {calibration.residualUs > 0 && <span className="tabular-nums">{` Spread ${ms(calibration.residualUs)}.`}</span>}
        {calibration.micsResidualUs !== undefined && calibration.micsResidualUs > 0 && (
          <span className="tabular-nums">{` Speaker microphones' spread ${ms(calibration.micsResidualUs)}.`}</span>
        )}
        {calibration.mode === 'mics' && calibration.members.some(m => m.code === 'no_capture') && ` ${NO_MIC_HINT}`}
      </p>
      <DataTable
        caption="Calibration"
        columns={withMics
          ? [
              ...COLUMNS,
              ...MICS_COLUMNS,
            ]
          : COLUMNS}
        rows={calibration.members.map(member => ({
          member,
          name: nameOf(member.deviceId),
          total,
        }))}
        rowKey={r => r.member.deviceId}
        empty={<p className="p-4 text-sm text-muted-foreground">No member answered.</p>}
      />
      {withMics && (
        <p className="text-sm text-muted-foreground">
          Speaker mics: what the speakers measured of the same chirps. Path to you: how much longer each one&apos;s sound
          takes to reach you than the others&apos;. Shown only; the trims come from what this device heard.
        </p>
      )}
      {onRetry && faint.length > 0 && (
        <div className="flex flex-col items-start gap-2">
          <Button
            type="button"
            variant="outline"
            aria-disabled={pending || !next || undefined}
            aria-describedby={noteId}
            onClick={retry}
          >
            {pending
              ? <Loader2Icon aria-hidden="true" className="animate-spin" />
              : <Volume2Icon aria-hidden="true" />}
            Retry louder
          </Button>
          <p id={noteId} className="text-sm text-muted-foreground">
            {next
              ? `${BOOST_NOTE} ${faint.map(m => nameOf(m.deviceId)).join(', ')}.`
              : BOOST_MAXED}
          </p>
          {phase && <p role="status" className="text-sm font-medium">{PHASES[phase]}</p>}
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        </div>
      )}
    </section>
  )
}
