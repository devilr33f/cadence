import type { ReactNode, Ref } from 'react'
import { cn } from 'cn'

// one stop of the wizard: its number down the left, as the timeline's stops, its title and what it asks; the title takes
// the focus (headingRef) when the step before it gives way
export function EnrollStep({ n, title, waiting = false, headingRef, children }: {
  n: number
  title: string
  waiting?: boolean
  headingRef?: Ref<HTMLHeadingElement>
  children: ReactNode
}) {
  return (
    <li className="grid grid-cols-[1.75rem_minmax(0,1fr)] gap-x-3">
      <span
        aria-hidden="true"
        className={cn(
          'grid size-7 place-items-center rounded-full border bg-card text-sm font-semibold tabular-nums',
          waiting && 'text-muted-foreground',
        )}
      >
        {n}
      </span>
      <div className="flex min-w-0 flex-col gap-3 pt-0.5">
        <h2
          ref={headingRef}
          tabIndex={headingRef
            ? -1
            : undefined}
          className={cn('text-base font-semibold outline-none', waiting && 'text-muted-foreground')}
        >
          <span className="sr-only">{`Step ${n}:`}</span>
          {' '}
          {title}
        </h2>
        {children}
      </div>
    </li>
  )
}
