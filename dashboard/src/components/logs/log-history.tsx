import type { InfiniteData, UseInfiniteQueryResult } from '@tanstack/react-query'
import type { LogViewHandle } from '@/components/data/log-view'
import type { LogPage } from '@/lib/log-paging'
import { ScrollTextIcon } from 'lucide-react'
import { useMemo, useRef } from 'react'
import { describeError } from '@/api/describe-error'
import { LogView } from '@/components/data/log-view'
import { MoreButton } from '@/components/data/more-button'
import { EmptyState, ErrorState, LoadingLines } from '@/components/data/states'
import { LOGS_MAX, LOGS_PAGE } from '@/lib/logs'
import { storedEntries } from './log-entries'

export const VIEWPORT = 'h-[60svh] min-h-72'

// stored lines, newest first, older pages on request up to the page's most
export function LogHistory({ history, names, timeZone, filtered }: {
  history: UseInfiniteQueryResult<InfiniteData<LogPage>>
  names: ReadonlyMap<string, string>
  timeZone: string | null
  filtered: boolean
}) {
  const viewRef = useRef<LogViewHandle>(null)
  const rows = useMemo(() => history.data && storedEntries(history.data.pages.flatMap(page => page.lines), names), [history.data, names])

  if (!rows) {
    if (history.isError) {
      return (
        <div className="rounded-lg border bg-card">
          <ErrorState title="The lines could not be read" error={history.error} onRetry={() => void history.refetch()} />
        </div>
      )
    }
    return <LoadingLines lines={6} label="Reading the lines" />
  }
  if (rows.length === 0) {
    return (
      <div className="rounded-lg border bg-card">
        <EmptyState
          icon={ScrollTextIcon}
          title="No lines"
          description={filtered
            ? 'No stored line matches this filter. Widen it, or turn Live on to wait for new ones.'
            : 'Speakers send their lines once they are enrolled, and the server\'s own lines join them.'}
        />
      </div>
    )
  }

  // ten pages at most, counted as pages: a page that repeats lines it already showed brings fewer than 200
  const full = (pages: number) => pages >= LOGS_MAX / LOGS_PAGE
  async function loadOlder() {
    const result = await history.fetchNextPage()
    // the button goes with the last page, or the last the page holds: the focus moves to the lines it brought
    if (!result.hasNextPage || full(result.data?.pages.length ?? 0))
      viewRef.current?.focus()
  }
  const capped = full(history.data!.pages.length)

  return (
    <div className="flex flex-col gap-3">
      <LogView ref={viewRef} records={rows} timeZone={timeZone} label="Stored lines" className={VIEWPORT} busy={history.isPlaceholderData} />
      {history.isFetchNextPageError && <p role="alert" className="text-sm text-destructive">{`The older lines could not be read. ${describeError(history.error)}`}</p>}
      {history.hasNextPage && !capped && (
        <MoreButton busy={history.isFetchingNextPage} onMore={() => void loadOlder()}>Load older</MoreButton>
      )}
      {history.hasNextPage && capped && (
        <p className="text-sm text-muted-foreground">The page holds 2,000 lines: set Until to read further back.</p>
      )}
    </div>
  )
}
