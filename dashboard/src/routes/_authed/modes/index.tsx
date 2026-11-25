import { createFileRoute } from '@tanstack/react-router'
import { ModesPage } from '@/components/modes/modes-page'
import { prefetchModes } from '@/lib/modes'

export const Route = createFileRoute('/_authed/modes/')({
  loader: ({ context }) => prefetchModes(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Modes · Cadence' },
    ],
  }),
  component: ModesPage,
})
