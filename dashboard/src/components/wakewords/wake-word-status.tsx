import type { WakeWordView } from '@/api/gen'
import { StatusBadge } from '@/components/data/status-badge'
import { trainCodeText } from '@/lib/wakewords'

const STAGES: Readonly<Record<string, string>> = {
  queued: 'Queued',
  embed: 'Listening to the takes',
  train: 'Training',
  calibrate: 'Calibrating',
}

function percent(v: number): string {
  return `${Math.round(v * 100)}%`
}

export function WakeWordStatus({ word }: { word: WakeWordView }) {
  if (word.status === 'ready')
    return <StatusBadge tone="success" label="Ready" />
  if (word.status === 'failed') {
    return (
      <span className="flex flex-col gap-0.5">
        <StatusBadge tone="danger" label="Failed" detail={word.code} />
        <span className="text-xs whitespace-normal text-muted-foreground">{trainCodeText(word.code)}</span>
      </span>
    )
  }
  const p = word.progress
  return (
    <StatusBadge
      tone="info"
      label={p
        ? STAGES[p.stage] ?? 'Training'
        : 'Training'}
      detail={p && p.stage !== 'queued'
        ? percent(p.fraction)
        : undefined}
    />
  )
}
