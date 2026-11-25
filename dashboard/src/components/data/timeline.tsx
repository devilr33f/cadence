import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from 'cn'
import { AlarmClockIcon, ChevronRightIcon, ListChecksIcon, MessageSquareTextIcon, MicIcon, MusicIcon, PlugIcon, SlidersHorizontalIcon, TriangleAlertIcon, WrenchIcon } from 'lucide-react'
import { clockText } from '@/lib/format-time'

export type EventKind = 'turn' | 'reply' | 'tool' | 'task' | 'config' | 'media' | 'alarm' | 'connection' | 'error'

export interface TimelineEntry {
  id: string
  at: string
  kind: EventKind
  text: ReactNode
  detail?: ReactNode
}

const KINDS: Record<EventKind, { icon: LucideIcon, label: string }> = {
  turn: { icon: MicIcon, label: 'Turn' },
  reply: { icon: MessageSquareTextIcon, label: 'Reply' },
  tool: { icon: WrenchIcon, label: 'Tool' },
  task: { icon: ListChecksIcon, label: 'Task' },
  config: { icon: SlidersHorizontalIcon, label: 'Config' },
  media: { icon: MusicIcon, label: 'Media' },
  alarm: { icon: AlarmClockIcon, label: 'Alarm' },
  connection: { icon: PlugIcon, label: 'Connection' },
  error: { icon: TriangleAlertIcon, label: 'Error' },
}

// a route's stop list: bold times down the left, a line through the stops, newest on top; the live stop is yellow
export function Timeline({ entries, live = false, timeZone, label }: {
  entries: readonly TimelineEntry[]
  live?: boolean
  // null while the household's zone loads: the times show placeholders
  timeZone: string | null
  label: string
}) {
  return (
    <ol aria-label={label} className="flex flex-col">
      {entries.map((entry, index) => {
        const { icon: Icon, label: kind } = KINDS[entry.kind]
        const head = index === 0 && live
        return (
          <li key={entry.id} className="grid grid-cols-[4.25rem_1.5rem_minmax(0,1fr)] gap-x-3 motion-safe:data-[arrived]:animate-[arrive_1.2s_ease-out]" data-arrived={head || undefined}>
            <time dateTime={entry.at} className="pt-0.5 text-sm font-semibold tabular-nums">
              {clockText(entry.at, timeZone)}
            </time>
            <div className="relative flex justify-center">
              <span
                aria-hidden="true"
                className={cn(
                  'absolute inset-y-0 w-px bg-border',
                  index === 0 && 'top-3',
                  index === entries.length - 1 && 'bottom-auto h-3',
                )}
              />
              <span
                className={cn(
                  'relative mt-0.5 grid size-6 place-items-center rounded-full border bg-card text-muted-foreground',
                  entry.kind === 'error' && 'border-destructive/40 bg-destructive-surface text-destructive',
                  head && 'border-[color-mix(in_oklch,var(--primary),var(--primary-foreground)_22%)] bg-primary text-primary-foreground',
                )}
              >
                <Icon aria-hidden="true" className="size-3.5" />
                <span className="sr-only">{kind}</span>
              </span>
            </div>
            <div className="min-w-0 pt-0.5 pb-4">
              {entry.detail
                ? (
                    <details className="group/stop">
                      <summary className="-mx-1.5 flex w-fit max-w-full cursor-pointer list-none items-start gap-1 rounded-sm px-1.5 text-sm transition-colors duration-150 hover:bg-muted/60 [&::-webkit-details-marker]:hidden">
                        <span className="min-w-0">{entry.text}</span>
                        <ChevronRightIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground transition-transform duration-150 group-open/stop:rotate-90" />
                      </summary>
                      <div className="mt-2 max-w-2xl rounded-md border bg-muted/40 p-3 text-sm">{entry.detail}</div>
                    </details>
                  )
                : <p className="text-sm">{entry.text}</p>}
            </div>
          </li>
        )
      })}
    </ol>
  )
}
