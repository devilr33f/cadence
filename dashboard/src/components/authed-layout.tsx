import { LiveFeedProvider } from '@/lib/live/live-provider'
import { AuthedShell } from './authed-shell'

// every page but the login: the shell, with the tab's live feed open while the reader is signed in
export function AuthedLayout() {
  return (
    <LiveFeedProvider>
      <AuthedShell />
    </LiveFeedProvider>
  )
}
