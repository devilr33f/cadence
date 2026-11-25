import { cn } from 'cn'

// the mark: three rising bars on the departure-poster yellow, a cadence and a speaker's level at once
export function Wordmark({ className, compact = false }: { className?: string, compact?: boolean }) {
  return (
    <span className={cn('inline-flex items-center gap-2 text-base font-semibold tracking-[-0.01em]', className)}>
      <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" className="size-5 shrink-0">
        <rect width="20" height="20" rx="4" fill="var(--primary)" />
        <rect x="4.5" y="10" width="2.5" height="5.5" rx="1" fill="var(--primary-foreground)" />
        <rect x="8.75" y="7" width="2.5" height="8.5" rx="1" fill="var(--primary-foreground)" />
        <rect x="13" y="4.5" width="2.5" height="11" rx="1" fill="var(--primary-foreground)" />
      </svg>
      {/* on a phone the header keeps only the mark */}
      <span className={cn(compact && 'max-sm:sr-only')}>Cadence</span>
    </span>
  )
}
