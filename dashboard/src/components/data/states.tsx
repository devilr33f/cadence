import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from 'cn'
import { RefreshCwIcon, TriangleAlertIcon } from 'lucide-react'
import { describeError } from '@/api/describe-error'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'

// an empty state teaches: what would be here, and the one action that puts it there
export function EmptyState({ icon: Icon, title, description, action, className }: {
  icon?: LucideIcon
  title: string
  description?: ReactNode
  action?: ReactNode
  className?: string
}) {
  return (
    <div className={cn('flex flex-col items-center gap-2 px-6 py-12 text-center', className)}>
      {Icon && <Icon aria-hidden="true" className="mb-1 size-5 text-muted-foreground" />}
      <p className="text-lg font-semibold">{title}</p>
      {description && <p className="max-w-[48ch] text-base text-muted-foreground">{description}</p>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  )
}

export function ErrorState({ title, error, onRetry, className }: {
  title: string
  error: unknown
  onRetry?: () => void
  className?: string
}) {
  return (
    <div role="alert" className={cn('flex flex-col items-center gap-2 px-6 py-12 text-center', className)}>
      <TriangleAlertIcon aria-hidden="true" className="mb-1 size-5 text-destructive" />
      <p className="text-lg font-semibold">{title}</p>
      <p className="max-w-[48ch] text-base text-muted-foreground">{describeError(error)}</p>
      {onRetry && (
        <Button variant="outline" className="mt-3" onClick={onRetry}>
          <RefreshCwIcon aria-hidden="true" />
          Try again
        </Button>
      )}
    </div>
  )
}

// a block's shape while it loads, so nothing jumps when the data lands
export function LoadingLines({ lines = 3, label, className }: { lines?: number, label: string, className?: string }) {
  return (
    <div role="status" aria-label={label} className={cn('flex flex-col gap-2.5', className)}>
      {Array.from({ length: lines }, (_, i) => `line-${i}`).map((key, i) => (
        <Skeleton
          key={key}
          className={cn('h-3.5', i === lines - 1
            ? 'w-2/5'
            : 'w-full')}
        />
      ))}
    </div>
  )
}
