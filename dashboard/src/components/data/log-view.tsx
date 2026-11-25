import type { ComponentProps, ReactNode, Ref } from 'react'
import type { Severity } from './severity-label'
import { useVirtualizer } from '@tanstack/react-virtual'
import { cn } from 'cn'
import { useImperativeHandle, useRef } from 'react'
import { clockText } from '@/lib/format-time'
import { SeverityLabel } from './severity-label'

export interface LogRecord {
  kind?: 'line'
  id: string
  at: string
  severity: Severity
  source: string
  // the speaker the line came from, where lines of several share the view
  device?: string
  message: string
  attrs?: Readonly<Record<string, string | number>>
}

// a note in the stream itself: where lines may be missing from it
export interface LogMark {
  kind: 'mark'
  id: string
  text: string
}

export type LogEntry = LogRecord | LogMark

export interface LogViewHandle {
  scrollToEnd: () => void
  focus: () => void
}

export function LogLine({ record, timeZone, ...props }: { record: LogRecord, timeZone: string | null } & ComponentProps<'li'>) {
  return (
    <li
      data-severity={record.severity}
      className="col-span-full grid grid-cols-subgrid items-baseline px-3 py-0.5 font-mono text-xs/5 hover:bg-muted/50"
      {...props}
    >
      <time dateTime={record.at} className="text-muted-foreground tabular-nums">{clockText(record.at, timeZone, true)}</time>
      <SeverityLabel severity={record.severity} />
      <span className="flex min-w-0 items-baseline gap-1.5">
        {record.device && <span className="truncate text-muted-foreground">{record.device}</span>}
        <span className="max-w-full truncate rounded-sm border px-1.5 leading-4 text-muted-foreground">{record.source}</span>
      </span>
      <span className="col-span-full break-words whitespace-pre-wrap sm:col-span-1">
        {record.message}
        {record.attrs && Object.entries(record.attrs).map(([key, value]) => (
          <span key={key} className="text-muted-foreground">{` ${key}=${value}`}</span>
        ))}
      </span>
    </li>
  )
}

function LogMarkLine({ mark, ...props }: { mark: LogMark } & ComponentProps<'li'>) {
  return (
    // padding, never margin: the virtualizer measures a row's border box
    <li className="col-span-full border-y border-dashed px-3 py-1 text-xs/5 text-warning" {...props}>
      {mark.text}
    </li>
  )
}

const LINE_ESTIMATE_PX = 20
// the box's own room above the first line and below the last
const EDGE_PX = 6
// how near the end a tail still counts as following: about a line
const END_PX = 24

// monospace lines on one shared grid (subgrid), so time, severity, source and message each line up in a column, the
// source's column a fixed width so nothing shifts as lines scroll by; only the lines in view (and a few around them)
// are in the page, so a view of thousands stays light. with `follow` it is a tail: anchored to its end, it keeps
// following appended lines while the reader is there
export function LogView({ records, timeZone, label, className, follow = false, busy = false, empty, onScrollAway, onScrollEnd, ref }: {
  records: readonly LogEntry[]
  timeZone: string | null
  label: string
  className?: string
  follow?: boolean
  // the lines are another read's while this one runs: faded, and said to be busy
  busy?: boolean
  // what the box says while it has no line yet
  empty?: ReactNode
  // the reader scrolled up, away from the end; or came back to it
  onScrollAway?: () => void
  onScrollEnd?: () => void
  ref?: Ref<LogViewHandle>
}) {
  const viewportRef = useRef<HTMLDivElement>(null)
  const lastTopRef = useRef(0)
  const virtualizer = useVirtualizer({
    count: records.length,
    getScrollElement: () => viewportRef.current,
    estimateSize: () => LINE_ESTIMATE_PX,
    getItemKey: index => records[index]!.id,
    overscan: 12,
    paddingStart: EDGE_PX,
    paddingEnd: EDGE_PX,
    anchorTo: follow ? 'end' : 'start',
    followOnAppend: follow,
    scrollEndThreshold: END_PX,
    // a render with no box to measure (the ssr server, the design preview) paints the first lines
    initialRect: { width: 0, height: 480 },
    // react 19 refuses a flushSync from inside a render's effects
    useFlushSync: false,
  })

  useImperativeHandle(ref, () => ({
    scrollToEnd: () => virtualizer.scrollToEnd(),
    focus: () => viewportRef.current?.focus(),
  }), [virtualizer])

  // only the reader scrolls up: a follow, and the lines growing, only ever move the view down. read from the box
  // itself, which the virtualizer may not have heard of yet
  function scrolled() {
    const viewport = viewportRef.current
    if (!viewport)
      return
    const top = viewport.scrollTop
    const up = top < lastTopRef.current
    lastTopRef.current = top
    if (viewport.scrollHeight - viewport.clientHeight - top <= END_PX)
      onScrollEnd?.()
    else if (up)
      onScrollAway?.()
  }

  const items = virtualizer.getVirtualItems()
  const before = items[0]?.start ?? EDGE_PX
  const after = Math.max(virtualizer.getTotalSize() - (items.at(-1)?.end ?? 0), EDGE_PX)
  return (
    <div
      ref={viewportRef}
      role="region"
      aria-label={label}
      // the list scrolls inside its own box, so the keyboard needs to reach it
      tabIndex={0}
      data-log-viewport=""
      aria-busy={busy || undefined}
      onScroll={scrolled}
      className={cn('overflow-auto rounded-lg border bg-card transition-opacity duration-200', busy && 'opacity-60', className)}
    >
      {records.length === 0 && empty && <p className="px-3 py-2 text-sm text-muted-foreground">{empty}</p>}
      <ol
        className="grid grid-cols-[auto_auto_minmax(0,1fr)] gap-x-3 sm:grid-cols-[auto_auto_10rem_minmax(0,1fr)]"
        style={{ paddingTop: before, paddingBottom: after }}
      >
        {items.map((item) => {
          const entry = records[item.index]!
          const props = {
            'data-index': item.index,
            'ref': virtualizer.measureElement,
            'aria-posinset': item.index + 1,
            'aria-setsize': records.length,
          }
          return entry.kind === 'mark'
            ? <LogMarkLine key={item.key} mark={entry} {...props} />
            : <LogLine key={item.key} record={entry} timeZone={timeZone} {...props} />
        })}
      </ol>
    </div>
  )
}
