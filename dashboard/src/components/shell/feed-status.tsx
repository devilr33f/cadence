import type { FeedState } from './shell-state'
import { StatusBadge } from '@/components/data/status-badge'

function label(feed: FeedState) {
  switch (feed.state) {
    case 'idle':
      return 'Not connected'
    case 'connecting':
      return 'Connecting'
    case 'live':
      return 'Live'
    case 'reconnecting':
      return feed.inSeconds === undefined
        ? 'Reconnecting'
        : `Reconnecting in ${feed.inSeconds} s`
    case 'closed':
      return 'Disconnected'
  }
}

// the live feed's tally: yellow while events stream in, a plain state otherwise; announced politely when it changes
export function FeedStatus({ feed }: { feed: FeedState }) {
  return (
    <p role="status" className="flex items-center">
      <span className="sr-only">Live feed: </span>
      {feed.state === 'live'
        ? (
            <span className="inline-flex h-6 items-center gap-1.5 rounded-full bg-primary px-2.5 text-sm font-medium text-primary-foreground">
              <span aria-hidden="true" className="size-2 rounded-full bg-primary-foreground motion-safe:animate-[tally_2.4s_ease-in-out_infinite]" />
              {label(feed)}
            </span>
          )
        : (
            <StatusBadge
              tone={feed.state === 'connecting'
                ? 'info'
                : feed.state === 'idle'
                  ? 'neutral'
                  : 'warning'}
              label={label(feed)}
            />
          )}
    </p>
  )
}
