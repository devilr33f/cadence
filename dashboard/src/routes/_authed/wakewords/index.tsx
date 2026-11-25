import { createFileRoute } from '@tanstack/react-router'
import { WakeWordsPage } from '@/components/wakewords/wake-words-page'
import { prefetchWakeWords } from '@/lib/wakewords'

export const Route = createFileRoute('/_authed/wakewords/')({
  loader: ({ context }) => prefetchWakeWords(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Wake words · Cadence' },
    ],
  }),
  component: WakeWordsPage,
})
