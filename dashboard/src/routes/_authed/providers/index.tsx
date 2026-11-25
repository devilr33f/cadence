import { createFileRoute } from '@tanstack/react-router'
import { ProvidersPage } from '@/components/providers/providers-page'
import { prefetchProviders } from '@/lib/providers'

export const Route = createFileRoute('/_authed/providers/')({
  loader: ({ context }) => prefetchProviders(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Providers · Cadence' },
    ],
  }),
  component: ProvidersPage,
})
