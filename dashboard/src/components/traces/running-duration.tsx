import { useNow } from '@/hooks/use-now'
import { spanDurationText } from '@/lib/traces/format'

// a length that keeps counting while its work runs: from start until now, never less than the server last said
export function RunningDuration({ start, durationMs, live }: { start: string | number, durationMs: number, live: boolean }) {
  const now = useNow(live)
  const from = typeof start === 'number'
    ? start
    : Date.parse(start)
  const ms = live && now !== null
    ? Math.max(durationMs, now - from)
    : durationMs
  return <span data-numeric className="tabular-nums">{spanDurationText(ms)}</span>
}
