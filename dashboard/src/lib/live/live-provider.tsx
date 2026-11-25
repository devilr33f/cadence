import type { ReactNode } from 'react'
import type { FeedStatus } from './feed'
import type { Invalidator } from './invalidation'
import type { Live } from './live-context'
import { useRouteContext } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { forgetDeletedElsewhere } from '@/lib/devices'
import { checkSession } from '@/lib/session'
import { createDeviceDeletions } from './deletions'
import { LiveFeed } from './feed'
import { createInvalidator } from './invalidation'
import { LiveContext, LiveStatusContext } from './live-context'
import { createSpanChannel } from './span-channel'
import { createLiveStore } from './store'
import { TOPIC_OF } from './topics'
import { liveUrl } from './url'

const DEVICE_DELETED = 'device_deleted'
const SPAN_DROPPED = 'span_dropped'

// the signed-in layout's feed: one socket per tab, its streams in memory for as long as the layout lives
export function LiveFeedProvider({ children }: { children: ReactNode }) {
  const { queryClient, api, returnToLogin } = useRouteContext({ from: '__root__' })
  const [status, setStatus] = useState<FeedStatus>('connecting')
  const [{ live, invalidator }] = useState<{ live: Live, invalidator: Invalidator }>(() => {
    const store = createLiveStore()
    const invalidator = createInvalidator(queryClient)
    const deletions = createDeviceDeletions()
    const spans = createSpanChannel()
    const feed = new LiveFeed({
      url: () => liveUrl(),
      onEvent: (event) => {
        store.record(event)
        // span events go to the traces page alone, which folds them into its own traces
        if (TOPIC_OF[event.type] === 'traces' || event.type === SPAN_DROPPED) {
          spans.emit({ kind: 'event', event })
          return
        }
        // the page that shows a deleted device lets go of it before its queries go; without one they go at once
        if (event.type === DEVICE_DELETED && event.deviceId) {
          invalidator.forget(event.deviceId)
          if (!deletions.take(event.deviceId))
            void forgetDeletedElsewhere(queryClient, api, event.deviceId)
        }
        invalidator.event(event)
      },
      onResync: (topics, cause) => {
        invalidator.topics(topics)
        // a tail that was following lines may have missed some: it says so where they went missing
        if (cause !== 'joined' && topics.includes('logs'))
          store.logGap(cause)
        if (topics.includes('traces'))
          spans.emit({ kind: 'gap', cause })
      },
      onStatus: setStatus,
      checkSession: () => checkSession(queryClient, api),
      onSignedOut: () => {
        void returnToLogin()
      },
      onRefused: (error, detail) => {
        // codes only, and only while developing (M7b-D4)
        if (import.meta.env.DEV)
          console.warn('live feed refused', error, detail ?? '')
      },
    })
    return {
      live: { feed, store, deletions, spans },
      invalidator,
    }
  })

  useEffect(() => {
    live.feed.start()
    return () => {
      live.feed.stop()
      invalidator.stop()
    }
  }, [live, invalidator])

  return (
    <LiveContext value={live}>
      <LiveStatusContext value={status}>
        {children}
      </LiveStatusContext>
    </LiveContext>
  )
}
