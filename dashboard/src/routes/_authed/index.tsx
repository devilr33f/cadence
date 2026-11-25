import { createFileRoute } from '@tanstack/react-router'
import { OverviewPage } from '@/components/overview/overview-page'
import { prefetchDevices } from '@/lib/devices'

export const Route = createFileRoute('/_authed/')({
  loader: ({ context }) => prefetchDevices(context.queryClient, context.api),
  component: OverviewPage,
})
