import type { ReactNode, RefObject } from 'react'
import type { DetailJson } from '@/api/gen'
import type { Topic } from '@/lib/live/topics'
import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { ChevronLeftIcon } from 'lucide-react'
import { useId, useRef } from 'react'
import { useApi } from '@/api/use-api'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { Stamp } from '@/components/data/stamp'
import { ErrorState, LoadingLines } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { useDeviceNames } from '@/hooks/use-device-names'
import { useLiveTopics } from '@/lib/live/use-live'
import { isActive, taskQueryOptions, useTaskActions } from '@/lib/tasks'
import { useTimeZone } from '@/lib/time-zone'
import { TaskHistory, TaskNotices } from './task-history'
import { TaskLiveView } from './task-live-view'
import { TaskQuestion } from './task-question'
import { TaskStatus } from './task-status'

const TOPICS: readonly Topic[] = ['tasks']

function Section({ title, children }: { title: string, children: ReactNode }) {
  const titleId = useId()
  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-3">
      <h2 id={titleId} className="text-lg font-semibold">{title}</h2>
      {children}
    </section>
  )
}

function BackLink() {
  return (
    <Link to="/tasks" className="mb-3 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ChevronLeftIcon aria-hidden="true" className="size-4" />
      Tasks
    </Link>
  )
}

// its kind, short id and speaker, when it started and ended
function Meta({ task }: { task: DetailJson }) {
  const timeZone = useTimeZone()
  const names = useDeviceNames()
  const device = task.deviceId && (names.get(task.deviceId) ?? task.deviceId)
  return (
    <p className="mt-1 text-sm text-muted-foreground">
      {`${[task.kind, task.shortId, device].filter(Boolean).join(' · ')} · started `}
      <Stamp at={task.createdAt} timeZone={timeZone} />
      {task.finishedAt && (
        <>
          {' · finished '}
          <Stamp at={task.finishedAt} timeZone={timeZone} />
        </>
      )}
    </p>
  )
}

function CancelTask({ taskId, finalFocus }: { taskId: string, finalFocus: RefObject<HTMLElement | null> }) {
  const actions = useTaskActions(taskId)
  return (
    <ConfirmDialog
      trigger={<Button type="button" variant="outline">Cancel the task</Button>}
      title="Cancel this task?"
      description="Hermes is told to stop it. What it already did stays done, and it cannot be resumed."
      confirmLabel="Cancel the task"
      tone="destructive"
      inlineError
      finalFocus={finalFocus}
      onConfirm={actions.cancel}
    />
  )
}

export function TaskPage() {
  const { id } = useParams({ from: '/_authed/tasks/$id' })
  useLiveTopics(TOPICS)
  // one route serves every task: nothing typed for one (a reply) may carry over to the next
  return <TaskView key={id} id={id} />
}

function TaskView({ id }: { id: string }) {
  const api = useApi()
  const detail = useQuery(taskQueryOptions(api, id))
  // what an answer or a cancel took its own button away from gives the focus to the title
  const titleRef = useRef<HTMLHeadingElement>(null)

  if (detail.isPending) {
    return (
      <>
        <BackLink />
        <LoadingLines lines={4} label="Loading the task" />
      </>
    )
  }
  if (detail.isError) {
    return (
      <>
        <BackLink />
        <div className="rounded-lg border bg-card">
          <ErrorState title="This task could not be shown" error={detail.error} onRetry={() => void detail.refetch()} />
        </div>
      </>
    )
  }

  const task = detail.data
  const question = task.status === 'waiting_input'
    ? task.question
    : null
  const notices = task.notices ?? []
  return (
    <>
      <BackLink />
      <div className="mb-8 flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-3">
            <h1 ref={titleRef} tabIndex={-1} className="text-xl font-semibold break-words outline-none">{task.title}</h1>
            <TaskStatus task={task} appearance="pill" />
          </div>
          <Meta task={task} />
        </div>
        {isActive(task) && <CancelTask taskId={task.shortId} finalFocus={titleRef} />}
      </div>
      <div className="flex max-w-(--content-max) flex-col gap-8">
        {question && (
          <Section title="Question">
            <TaskQuestion taskId={task.shortId} question={question} done={titleRef} />
          </Section>
        )}
        <Section title="Live view">
          <TaskLiveView taskId={task.shortId} />
        </Section>
        {task.result && (
          <Section title="Result">
            <p className="max-w-[65ch] break-words whitespace-pre-wrap">{task.result}</p>
          </Section>
        )}
        <Section title="History">
          <TaskHistory events={task.events ?? []} />
        </Section>
        {notices.length > 0 && (
          <Section title="Notices">
            <TaskNotices notices={notices} />
          </Section>
        )}
      </div>
    </>
  )
}
