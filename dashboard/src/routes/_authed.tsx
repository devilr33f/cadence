import { createFileRoute } from '@tanstack/react-router'
import { AuthedLayout } from '@/components/authed-layout'
import { LayoutError } from '@/components/layout-error'
import { requireSession } from '@/lib/auth-guard'
import { settingsQueryOptions } from '@/lib/time-zone'

export const Route = createFileRoute('/_authed')({
  beforeLoad: requireSession,
  // the household's zone for every time on the page; a failure leaves the placeholders
  loader: ({ context }) => context.queryClient.prefetchQuery({
    ...settingsQueryOptions(context.api),
    retry: false,
  }),
  component: AuthedLayout,
  errorComponent: LayoutError,
})
