import { createFileRoute } from '@tanstack/react-router'
import { EnrollPage } from '@/components/devices/enroll/enroll-page'
import { boardsQueryOptions } from '@/lib/devices'

export const Route = createFileRoute('/_authed/devices/enroll')({
  loader: ({ context }) => context.queryClient.prefetchQuery({
    ...boardsQueryOptions(context.api),
    retry: false,
  }),
  head: () => ({
    meta: [
      { title: 'Enroll a speaker · Cadence' },
    ],
  }),
  component: EnrollPage,
})
