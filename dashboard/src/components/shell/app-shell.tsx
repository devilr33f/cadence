import type { ReactNode } from 'react'
import type { SectionBadges } from './nav-sections'
import type { AccountState, FeedState } from './shell-state'
import { Toaster } from '@/components/ui/toast'
import { AppHeader } from './app-header'
import { MainNav } from './main-nav'
import { Wordmark } from './wordmark'

export function AppShell({ feed, account, badges, status, children }: {
  feed: FeedState
  account: AccountState
  // what the header shows beside the feed's tally (the busy menu, which reads the api)
  status?: ReactNode
  // counts beside the sections, such as the memory suggestions waiting for review
  badges?: SectionBadges
  children: ReactNode
}) {
  return (
    <Toaster>
      <a
        href="#main"
        className="sr-only z-50 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:px-3 focus:py-2"
      >
        Skip to content
      </a>
      <div className="flex min-h-svh">
        <aside aria-label="Sidebar" className="sticky top-0 hidden h-svh w-(--rail-width) shrink-0 flex-col overflow-y-auto bg-sidebar text-sidebar-foreground [scrollbar-color:var(--sidebar-border)_transparent] [--ring:var(--sidebar-ring)] md:flex">
          <div className="flex h-(--header-height) shrink-0 items-center px-5 text-sidebar-accent-foreground">
            <Wordmark />
          </div>
          <MainNav badges={badges} />
        </aside>
        <div className="flex min-w-0 flex-1 flex-col">
          <AppHeader feed={feed} account={account} badges={badges} status={status} />
          <main id="main" tabIndex={-1} className="mx-auto w-full max-w-(--content-max) flex-1 px-4 py-6 outline-none md:px-8 md:py-8">
            {children}
          </main>
        </div>
      </div>
    </Toaster>
  )
}
