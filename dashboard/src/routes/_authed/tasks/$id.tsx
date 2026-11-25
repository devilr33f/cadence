import { createFileRoute } from '@tanstack/react-router'
import { TaskPage } from '@/components/tasks/task-page'
import { prefetchTask } from '@/lib/tasks'

export const Route = createFileRoute('/_authed/tasks/$id')({
  loader: ({ context, params }) => prefetchTask(context.queryClient, context.api, params.id),
  head: () => ({
    meta: [
      { title: 'Task · Cadence' },
    ],
  }),
  component: TaskPage,
})
