import { createFileRoute } from '@tanstack/react-router'
import { DevicePage } from '@/components/devices/detail/device-page'
import { prefetchDevicePage } from '@/lib/devices'

export const Route = createFileRoute('/_authed/devices/$id')({
  loader: ({ context, params }) => prefetchDevicePage(context.queryClient, context.api, params.id),
  head: () => ({
    meta: [
      { title: 'Device · Cadence' },
    ],
  }),
  component: DevicePage,
})
