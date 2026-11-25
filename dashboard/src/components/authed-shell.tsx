import type { SectionBadges } from '@/components/shell/nav-sections'
import { Outlet } from '@tanstack/react-router'
import { AppShell } from '@/components/shell/app-shell'
import { BusyMenu } from '@/components/shell/busy-menu'
import { useSignOut } from '@/hooks/use-sign-out'
import { useLiveStatus } from '@/lib/live/use-live'
import { suggestionsBadge, useSuggestionCount } from '@/lib/memory'

export function AuthedShell() {
  const status = useLiveStatus()
  const signOut = useSignOut()
  const suggestions = useSuggestionCount()
  const badges: SectionBadges = {
    memory: suggestionsBadge(suggestions),
  }
  return (
    <AppShell feed={{ state: status }} account={{ state: 'signed-in', onSignOut: signOut }} badges={badges} status={<BusyMenu />}>
      <Outlet />
    </AppShell>
  )
}
