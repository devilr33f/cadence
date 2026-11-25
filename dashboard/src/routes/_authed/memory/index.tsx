import { createFileRoute } from '@tanstack/react-router'
import { MemoryPage } from '@/components/memory/memory-page'
import { prefetchMemoryPage } from '@/lib/memory'

export const Route = createFileRoute('/_authed/memory/')({
  loader: ({ context }) => prefetchMemoryPage(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Memory · Cadence' },
    ],
  }),
  component: MemoryPage,
})
