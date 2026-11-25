import type { TraceListState } from './trace-list-reader'
import type { Topic } from '@/lib/live/topics'
import { useEffect, useRef, useState } from 'react'
import { useApi } from '@/api/use-api'
import { useLiveTopics, useSpanSignals } from '@/lib/live/use-live'
import { INITIAL_LIST, TraceListReader } from './trace-list-reader'

const TOPICS: readonly Topic[] = ['traces']

export interface TraceList extends TraceListState {
  retry: () => void
}

// the list page's traces, kept current by the traces topic while the page shows; `device` narrows it on the server
export function useTraceList(device: string): TraceList {
  const api = useApi()
  useLiveTopics(TOPICS)
  const [state, setState] = useState<TraceListState>(INITIAL_LIST)
  const [attempt, setAttempt] = useState(0)
  const readerRef = useRef<TraceListReader | null>(null)

  useEffect(() => {
    const next = new TraceListReader(api, device, setState)
    readerRef.current = next
    next.start()
    return () => {
      next.stop()
      if (readerRef.current === next)
        readerRef.current = null
    }
  }, [api, device, attempt])

  useSpanSignals(signal => readerRef.current?.signal(signal))

  return {
    ...state,
    retry: () => setAttempt(n => n + 1),
  }
}
