import { createFileRoute } from '@tanstack/react-router'
import { IntegrationsPage } from '@/components/integrations/integrations-page'
import { prefetchIntegrations } from '@/lib/integrations'

export const Route = createFileRoute('/_authed/integrations/')({
  loader: ({ context }) => prefetchIntegrations(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Integrations · Cadence' },
    ],
  }),
  component: IntegrationsPage,
})
