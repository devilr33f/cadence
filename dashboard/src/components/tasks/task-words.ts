import type { Tone } from '@/components/data/tone'

// each status the server names (tasks.Status*): its words and its tone; one waiting for the owner is worded in amber
export const STATUS: Readonly<Record<string, { label: string, tone: Tone }>> = {
  queued: { label: 'Queued', tone: 'neutral' },
  running: { label: 'Running', tone: 'info' },
  waiting_input: { label: 'Waiting for an answer', tone: 'warning' },
  done: { label: 'Done', tone: 'success' },
  failed: { label: 'Failed', tone: 'danger' },
  cancelled: { label: 'Cancelled', tone: 'neutral' },
  unknown: { label: 'Unknown', tone: 'warning' },
}

export function statusLabel(status: string): string {
  return STATUS[status]?.label ?? status
}

// the filter's choices: every status, and the active ones together (what may still be cancelled)
export const STATUS_FILTERS: readonly { value: string, label: string }[] = [
  { value: '', label: 'All' },
  { value: 'queued,running,waiting_input,unknown', label: 'Active' },
  ...[
    'waiting_input',
    'running',
    'queued',
    'done',
    'failed',
    'cancelled',
    'unknown',
  ].map(status => ({
    value: status,
    label: statusLabel(status),
  })),
]
