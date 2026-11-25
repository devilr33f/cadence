import type { ReactNode } from 'react'
import { cn } from 'cn'

// a label on a row (a token's scope, a memory's scope): a small bordered tag, rounded as a control, never a status pill
export function Tag({ mono = false, className, children }: { mono?: boolean, className?: string, children: ReactNode }) {
  return (
    <span className={cn('inline-flex h-6 items-center rounded-md border bg-muted px-2 text-xs whitespace-nowrap', mono ? 'font-mono' : 'font-medium', className)}>
      {children}
    </span>
  )
}
