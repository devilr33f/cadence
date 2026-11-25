import { useNow } from '@/hooks/use-now'
import { durationText } from '@/lib/format-time'

// how long the screen's code is still good, as the screen counts it
export function ClaimCountdown({ expiresAt }: { expiresAt: string }) {
  const now = useNow()
  if (now === null)
    return <span className="tabular-nums text-muted-foreground">Expires soon</span>
  const left = Date.parse(expiresAt) - now
  return left <= 0
    ? <span className="text-warning">Expired</span>
    : <span className="tabular-nums text-muted-foreground">{`${durationText(left)} left`}</span>
}
