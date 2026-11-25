import type { ReactNode } from 'react'
import type { SectionBadges } from './nav-sections'
import type { AccountState, FeedState } from './shell-state'
import { AccountStatus } from './account-status'
import { FeedStatus } from './feed-status'
import { MobileNav } from './mobile-nav'
import { ThemeToggle } from './theme-toggle'
import { Wordmark } from './wordmark'

export function AppHeader({ feed, account, badges, status }: {
  feed: FeedState
  account: AccountState
  badges?: SectionBadges
  // the signed-in shell's own state beside the feed's tally (the busy menu)
  status?: ReactNode
}) {
  return (
    <header className="sticky top-0 z-30 flex h-(--header-height) shrink-0 items-center gap-2 border-b bg-background px-3 md:px-6">
      <MobileNav badges={badges} />
      <Wordmark compact className="md:hidden" />
      <div className="ml-auto flex min-w-0 items-center gap-2 sm:gap-3">
        {status}
        <FeedStatus feed={feed} />
        <span aria-hidden="true" className="h-4 w-px bg-border max-sm:hidden" />
        <AccountStatus account={account} />
        <ThemeToggle />
      </div>
    </header>
  )
}
