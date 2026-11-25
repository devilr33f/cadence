import { createFileRoute } from '@tanstack/react-router'
import { GroupsPage } from '@/components/groups/groups-page'
import { prefetchGroups } from '@/lib/groups'

export const Route = createFileRoute('/_authed/groups/')({
  loader: ({ context }) => prefetchGroups(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Groups · Cadence' },
    ],
  }),
  component: GroupsPage,
})
