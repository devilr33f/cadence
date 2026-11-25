import { createFileRoute } from '@tanstack/react-router'
import { ClaimsPage } from '@/components/claims/claims-page'
import { prefetchClaims } from '@/lib/claims'

export const Route = createFileRoute('/_authed/claims/')({
  loader: ({ context }) => prefetchClaims(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Screens waiting · Cadence' },
    ],
  }),
  component: ClaimsPage,
})
