import type { FeedStatus } from './feed'
import type { Live } from './live-context'
import type { SpanSignal } from './span-channel'
import type { StreamEvent } from './store'
import type { Topic } from './topics'
import { use, useEffect, useEffectEvent, useSyncExternalStore } from 'react'
import { LiveContext, LiveStatusContext } from './live-context'

const NOTHING: readonly StreamEvent[] = []
const nothing = () => NOTHING

function useLive(): Live {
  const live = use(LiveContext)
  if (!live)
    throw new Error('the live feed is used outside its provider')
  return live
}

export function useLiveStatus(): FeedStatus {
  return use(LiveStatusContext)
}

// a page's topics while it is mounted; the feed subscribes to every mounted page's union
export function useLiveTopics(topics: readonly Topic[]) {
  const { feed } = useLive()
  const key = topics.join(',')
  useEffect(() => feed.want(key === ''
    ? []
    : key.split(',') as Topic[]), [feed, key])
}

// a page that shows a device hears of its deletion elsewhere while it is mounted, and takes it over: it stops reading
// the device, then drops its queries
export function useDeviceDeleted(deviceId: string, onDeleted: () => void) {
  const { deletions } = useLive()
  const deleted = useEffectEvent(onDeleted)
  useEffect(() => deletions.watch(deviceId, () => deleted()), [deletions, deviceId])
}

// the timeline's events, newest first, at most 500; empty on the ssr server
export function useLiveTimeline(): readonly StreamEvent[] {
  const { store } = useLive()
  return useSyncExternalStore(store.timeline.subscribe, store.timeline.snapshot, nothing)
}

// the log tail's lines, newest first, at most 2000; they come only while a page subscribes to logs
export function useLiveLogs(): readonly StreamEvent[] {
  const { store } = useLive()
  return useSyncExternalStore(store.logs.subscribe, store.logs.snapshot, nothing)
}

// a traces page's span events and gaps while it is mounted; it subscribes to the traces topic itself
export function useSpanSignals(onSignal: (signal: SpanSignal) => void) {
  const { spans } = useLive()
  const signal = useEffectEvent(onSignal)
  useEffect(() => spans.subscribe(s => signal(s)), [spans])
}
