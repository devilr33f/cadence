import { createFileRoute } from '@tanstack/react-router'
import { TasksPage } from '@/components/tasks/tasks-page'
import { prefetchTasks } from '@/lib/tasks'

export const Route = createFileRoute('/_authed/tasks/')({
  loader: ({ context }) => prefetchTasks(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Tasks · Cadence' },
    ],
  }),
  component: TasksPage,
})
