import { createFileRoute } from '@tanstack/react-router'
import { HistoryPage } from '@/components/history/history-page'

// no loader: third-party titles and artwork references stay in the browser
export const Route = createFileRoute('/_authed/history/')({
  head: () => ({
    meta: [
      { title: 'History · Cadence' },
    ],
  }),
  component: HistoryPage,
})
