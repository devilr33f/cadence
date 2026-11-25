import { createFileRoute } from '@tanstack/react-router'
import { DevicesPage } from '@/components/devices/devices-page'
import { prefetchDeviceList } from '@/lib/devices'

export const Route = createFileRoute('/_authed/devices/')({
  loader: ({ context }) => prefetchDeviceList(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Devices · Cadence' },
    ],
  }),
  component: DevicesPage,
})
