import { createFileRoute } from '@tanstack/react-router'
import { GroupPage } from '@/components/groups/group-page'
import { prefetchGroup } from '@/lib/groups'

export const Route = createFileRoute('/_authed/groups/$id')({
  loader: ({ context, params }) => prefetchGroup(context.queryClient, context.api, params.id),
  head: () => ({
    meta: [
      { title: 'Group · Cadence' },
    ],
  }),
  component: GroupPage,
})
