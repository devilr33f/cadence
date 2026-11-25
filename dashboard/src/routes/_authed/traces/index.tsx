import { createFileRoute } from '@tanstack/react-router'
import { TracesPage } from '@/components/traces/traces-page'
import { prefetchDevices } from '@/lib/devices'

export interface TracesSearch {
  device?: string
}

// a speaker's id narrows the list; anything else in the address is let go
export function validateTracesSearch(search: Record<string, unknown>): TracesSearch {
  const device = search.device
  return typeof device === 'string' && /^[\w-]{1,64}$/.test(device)
    ? { device }
    : {}
}

// no loader for the traces: the list is read in the browser and kept current there by the feed
export const Route = createFileRoute('/_authed/traces/')({
  validateSearch: validateTracesSearch,
  loader: ({ context }) => prefetchDevices(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Traces · Cadence' },
    ],
  }),
  component: TracesPage,
})
