import type { DeviceDeletions } from './deletions'
import type { FeedStatus, LiveFeed } from './feed'
import type { SpanChannel } from './span-channel'
import type { LiveStore } from './store'
import { createContext } from 'react'

export interface Live {
  feed: LiveFeed
  store: LiveStore
  deletions: DeviceDeletions
  spans: SpanChannel
}

export const LiveContext = createContext<Live | null>(null)

// the ssr server and the browser's first render both say connecting: the browser connects at once
export const LiveStatusContext = createContext<FeedStatus>('connecting')
