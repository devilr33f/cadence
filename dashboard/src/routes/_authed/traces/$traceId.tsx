import { createFileRoute } from '@tanstack/react-router'
import { TracePage } from '@/components/traces/trace-page'
import { prefetchDevices } from '@/lib/devices'

// no loader for the trace: it is read in the browser and kept current there by the feed while it runs
export const Route = createFileRoute('/_authed/traces/$traceId')({
  loader: ({ context }) => prefetchDevices(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Trace · Cadence' },
    ],
  }),
  component: TracePage,
})
