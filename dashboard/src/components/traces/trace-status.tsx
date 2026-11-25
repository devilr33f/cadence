import { StatusBadge } from '@/components/data/status-badge'

// a trace's or a span's state in words, glyph and colour: running, failed or done
export function TraceStatus({ live, error, appearance }: { live: boolean, error: boolean, appearance?: 'inline' | 'pill' }) {
  if (error && !live)
    return <StatusBadge tone="danger" label="Error" appearance={appearance} />
  if (live) {
    return (
      <StatusBadge
        tone="info"
        label="Running"
        appearance={appearance}
        {...(error && { detail: 'with an error' })}
      />
    )
  }
  return <StatusBadge tone="success" label="OK" appearance={appearance} />
}
