import { createFileRoute } from '@tanstack/react-router'
import { TokensPage } from '@/components/tokens/tokens-page'
import { prefetchTokens } from '@/lib/tokens'

export const Route = createFileRoute('/_authed/tokens/')({
  loader: ({ context }) => prefetchTokens(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'API tokens · Cadence' },
    ],
  }),
  component: TokensPage,
})
