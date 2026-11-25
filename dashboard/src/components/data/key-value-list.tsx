import type { ReactNode } from 'react'
import { cn } from 'cn'

export interface KeyValue {
  label: string
  value: ReactNode
  // ids, codes and hashes: monospace, and they wrap anywhere rather than overflow
  mono?: boolean
}

export function KeyValueList({ items, className }: { items: readonly KeyValue[], className?: string }) {
  return (
    <dl className={cn('grid grid-cols-[minmax(7rem,max-content)_1fr] gap-x-6 gap-y-2.5 text-sm', className)}>
      {items.map(({ label, value, mono }) => (
        <div key={label} className="contents">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className={cn('min-w-0', mono && 'font-mono text-xs/5 break-all')}>{value}</dd>
        </div>
      ))}
    </dl>
  )
}
