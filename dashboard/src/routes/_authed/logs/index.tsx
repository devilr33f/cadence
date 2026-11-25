import { createFileRoute } from '@tanstack/react-router'
import { LogsPage } from '@/components/logs/logs-page'

// no loader: lines may carry what third-party programs wrote, so the browser reads them, never the ssr server
export const Route = createFileRoute('/_authed/logs/')({
  head: () => ({
    meta: [
      { title: 'Logs · Cadence' },
    ],
  }),
  component: LogsPage,
})
