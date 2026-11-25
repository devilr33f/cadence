import { createFileRoute } from '@tanstack/react-router'
import { ClaimPage } from '@/components/claims/claim-page'

export const Route = createFileRoute('/_authed/claims/$code')({
  head: () => ({
    meta: [
      { title: 'Approve a screen · Cadence' },
    ],
  }),
  component: ClaimPage,
})
