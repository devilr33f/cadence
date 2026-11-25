import type { LogViewHandle } from '@/components/data/log-view'
import type { StreamEvent } from '@/lib/live/store'
import type { Topic } from '@/lib/live/topics'
import type { LogFilter } from '@/lib/logs'
import { useEffect, useMemo, useRef, useState } from 'react'
import { LogView } from '@/components/data/log-view'
import { Button } from '@/components/ui/button'
import { useLiveLogs, useLiveTopics } from '@/lib/live/use-live'
import { linesAfter, liveEntries } from './log-entries'
import { VIEWPORT } from './log-history'

const LOGS: readonly Topic[] = ['logs']

// lines as the server stores them, from the moment it opens: the logs topic is wanted only while it is mounted (the
// toggle mounts it, the page's unmount drops it), its lines are the feed's own buffer of the newest 2,000, and while
// the reader is scrolled up it holds what it showed (the buffer goes on, the view does not grow) until they come back
// to the end or jump there
export function LiveTail({ filter, names, timeZone }: {
  filter: LogFilter
  names: ReadonlyMap<string, string>
  timeZone: string | null
}) {
  useLiveTopics(LOGS)
  const events = useLiveLogs()
  // lines from an earlier tail have a gap after them: this one starts with the next line
  const [since] = useState(() => (events[0]?.seq ?? -1) + 1)
  const [frozen, setFrozen] = useState<readonly StreamEvent[] | null>(null)
  const [jumps, setJumps] = useState(0)
  const viewRef = useRef<LogViewHandle>(null)
  const shown = frozen ?? events
  const entries = useMemo(() => liveEntries(shown, since, filter, names), [shown, since, filter, names])
  const waiting = frozen
    ? linesAfter(events, frozen[0]?.seq ?? since - 1, filter)
    : 0

  useEffect(() => {
    if (jumps === 0)
      return
    viewRef.current?.scrollToEnd()
    // the button that asked goes with the pause
    viewRef.current?.focus()
  }, [jumps])

  return (
    <div className="flex flex-col gap-2">
      <div className="flex min-h-8 flex-wrap items-center gap-x-3 gap-y-1">
        <p role="status" aria-label="Live tail" className="text-sm font-medium">
          {frozen ? 'Paused while you read.' : 'Following new lines.'}
        </p>
        {frozen && (
          <>
            <span className="text-sm text-muted-foreground tabular-nums">
              {`${waiting.toLocaleString('en-GB')} new ${waiting === 1
                ? 'line'
                : 'lines'}`}
            </span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                setFrozen(null)
                setJumps(n => n + 1)
              }}
            >
              Jump to the latest lines
            </Button>
          </>
        )}
      </div>
      <LogView
        ref={viewRef}
        records={entries}
        timeZone={timeZone}
        label="Live lines"
        className={VIEWPORT}
        follow
        empty="Waiting for new lines"
        onScrollAway={() => setFrozen(current => current ?? events)}
        onScrollEnd={() => setFrozen(null)}
      />
    </div>
  )
}
