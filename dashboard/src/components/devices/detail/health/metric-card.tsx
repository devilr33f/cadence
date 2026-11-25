import type { UseQueryResult } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import type { MetricsRead } from '@/lib/metrics'
import { cn } from 'cn'
import { useId } from 'react'
import { describeError } from '@/api/describe-error'
import { LoadingLines } from '@/components/data/states'

// one metric's card: its title, the latest value as its figure, then what the read drew. a read that failed is worded
// in codes; another window's read keeps the card as it was, faded, until its points land
export function MetricCard({ title, read, children }: {
  title: string
  read: UseQueryResult<MetricsRead>
  children: (read: MetricsRead) => ReactNode
}) {
  const titleId = useId()
  let body: ReactNode
  if (read.data)
    body = children(read.data)
  else if (read.isError)
    body = <p role="alert" className="text-sm text-destructive">{describeError(read.error)}</p>
  else
    body = <LoadingLines lines={4} label={`Reading ${title.toLowerCase()}`} />
  return (
    <section aria-labelledby={titleId} className={cn('flex min-w-0 flex-col gap-3 rounded-lg border bg-card p-4 transition-opacity duration-200', read.isPlaceholderData && 'opacity-60')}>
      <h3 id={titleId} className="text-base font-semibold">{title}</h3>
      {body}
      {read.data?.body.truncated && <p className="text-sm text-muted-foreground">The server sent the first 50,000 points of this window.</p>}
    </section>
  )
}

// the latest value, large, with what it is beside it
export function Figure({ value, note }: { value: string, note?: string }) {
  return (
    <p className="flex flex-wrap items-baseline gap-x-2">
      <span className="text-2xl font-semibold">{value}</span>
      {note && <span className="text-sm text-muted-foreground">{note}</span>}
    </p>
  )
}

export function NoPoints({ children = 'No points in this window.' }: { children?: string }) {
  return <p className="text-sm text-muted-foreground">{children}</p>
}
