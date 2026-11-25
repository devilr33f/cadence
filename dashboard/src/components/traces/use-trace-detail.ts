import type { SpanView, TraceView } from '@/api/gen'
import type { Topic } from '@/lib/live/topics'
import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState } from 'react'
import { ApiError } from '@/api/error'
import { useApi } from '@/api/use-api'
import { useLiveTopics, useSpanSignals } from '@/lib/live/use-live'
import { traceQueryOptions } from '@/lib/traces/queries'
import { droppedCount, readSpanEvent } from '@/lib/traces/span-event'
import { withFeedSpans } from '@/lib/traces/trace-view'

const TOPICS: readonly Topic[] = ['traces']
const FLUSH_MS = 250

export interface TraceDetail {
  view: TraceView | undefined
  // read and not found: the server's ring let it go (or it never was)
  gone: boolean
  loading: boolean
  error: unknown
  retry: () => void
}

function notFound(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404
}

// one trace as read, then kept current by the traces topic's spans of it while the page shows; a gap of the feed reads
// it again
export function useTraceDetail(id: string): TraceDetail {
  const api = useApi()
  useLiveTopics(TOPICS)
  const query = useQuery({
    ...traceQueryOptions(api, id),
    retry: (count, error) => !notFound(error) && count < 2,
  })
  const pendingRef = useRef(new Map<string, SpanView>())
  // what the page shows of the feed's spans: the ref's copy as of its last flush
  const [feed, setFeed] = useState<ReadonlyMap<string, SpanView>>(() => new Map())
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  useEffect(() => () => clearTimeout(timerRef.current), [])

  // a read holds every span up to its seq: the feed's older copies go
  const read = query.data
  useEffect(() => {
    if (!read)
      return
    for (const [spanId, span] of pendingRef.current) {
      if (span.seq <= read.seq)
        pendingRef.current.delete(spanId)
    }
  }, [read])

  useSpanSignals((signal) => {
    if (signal.kind === 'gap' || (signal.kind === 'event' && droppedCount(signal.event) !== undefined)) {
      void query.refetch()
      return
    }
    const event = readSpanEvent(signal.event)
    if (!event || event.span.traceId !== id)
      return
    const held = pendingRef.current.get(event.span.spanId)
    if (held && held.seq >= event.span.seq)
      return
    pendingRef.current.set(event.span.spanId, event.span)
    timerRef.current ??= setTimeout(() => {
      timerRef.current = undefined
      setFeed(new Map(pendingRef.current))
    }, FLUSH_MS)
  })

  const view = useMemo(() => withFeedSpans(read, feed.values()), [read, feed])
  const gone = query.isError && notFound(query.error) && view === undefined
  return {
    view,
    gone,
    loading: query.isPending && view === undefined,
    error: query.isError && !gone && view === undefined
      ? query.error
      : null,
    retry: () => void query.refetch(),
  }
}
