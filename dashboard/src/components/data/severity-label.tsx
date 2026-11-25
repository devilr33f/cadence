import type { Tone } from './tone'
import { StatusBadge } from './status-badge'

export type Severity = 'debug' | 'info' | 'warn' | 'error'

const SEVERITIES: Record<Severity, Tone> = {
  debug: 'neutral',
  info: 'info',
  warn: 'warning',
  error: 'danger',
}

export function SeverityLabel({ severity }: { severity: Severity }) {
  return (
    <StatusBadge
      tone={SEVERITIES[severity]}
      label={severity === 'warn'
        ? 'WARN'
        : severity.toUpperCase()}
      className="w-14 font-mono text-xs font-medium"
    />
  )
}
