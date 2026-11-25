import type { RefObject } from 'react'
import type { InboxItem } from '@/api/gen'
import type { Tone } from '@/components/data/tone'
import type { Topic } from '@/lib/live/topics'
import type { InboxState, TopicGroup } from '@/lib/subscriptions'
import { useQuery } from '@tanstack/react-query'
import { InboxIcon } from 'lucide-react'
import { useId, useRef } from 'react'
import { useApi } from '@/api/use-api'
import { Stamp } from '@/components/data/stamp'
import { EmptyState, ErrorState, LoadingLines } from '@/components/data/states'
import { StatusBadge } from '@/components/data/status-badge'
import { Tag } from '@/components/data/tag'
import { useLiveTopics } from '@/lib/live/use-live'
import { EVENT_TEXT, groupByTopic, inboxQueryOptions, inboxState, isOpenQuestion } from '@/lib/subscriptions'
import { useTimeZone } from '@/lib/time-zone'
import { ApprovalReply } from './approval-reply'
import { QuestionReply } from './question-reply'

// subscription_update, subscription_inbox: an item held, told, read or answered
const TOPICS: readonly Topic[] = ['subscriptions']

const STATE: Record<InboxState, { tone: Tone, label: string }> = {
  held: { tone: 'warning', label: 'Held while busy' },
  pending: { tone: 'info', label: 'Waiting to be told' },
  unread: { tone: 'neutral', label: 'Unread' },
  read: { tone: 'success', label: 'Read' },
}

// outside text: the system's own words, set as text that wraps, never as markup
function Words({ children }: { children: string }) {
  return <span className="break-words whitespace-pre-wrap">{children}</span>
}

function eventText(event: string): string {
  return EVENT_TEXT[event as keyof typeof EVENT_TEXT] ?? event
}

function OpenQuestion({ deviceId, item, done }: { deviceId: string, item: InboxItem & { question: NonNullable<InboxItem['question']> }, done: RefObject<HTMLElement | null> }) {
  const timeZone = useTimeZone()
  const titleId = useId()
  const question = item.question
  return (
    <li aria-labelledby={titleId} className="flex flex-col gap-4 rounded-lg border bg-card p-5">
      <div className="flex flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          <Stamp at={item.at} timeZone={timeZone} />
          <Tag>
            {question.kind === 'approval'
              ? 'Approval'
              : 'Question'}
          </Tag>
          <code className="min-w-0 font-mono text-xs break-all">{item.topic}</code>
        </div>
        {item.title && <h4 className="text-base font-semibold"><Words>{item.title}</Words></h4>}
      </div>
      <p id={titleId} className="text-base"><Words>{question.text}</Words></p>
      {/* an answer belongs to its question: a new token starts an empty form, so a draft never goes out with another's */}
      {question.kind === 'approval'
        ? <ApprovalReply key={question.token} deviceId={deviceId} question={question} done={done} />
        : <QuestionReply key={question.token} deviceId={deviceId} question={question} done={done} />}
    </li>
  )
}

function Group({ group }: { group: TopicGroup }) {
  const timeZone = useTimeZone()
  const titleId = useId()
  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-2">
      <div>
        <h4 id={titleId} className="text-base font-semibold">
          {group.title
            ? <Words>{group.title}</Words>
            : <code className="font-mono text-sm break-all">{group.topic}</code>}
        </h4>
        {group.title && <code className="font-mono text-xs break-all text-muted-foreground">{group.topic}</code>}
      </div>
      <ol className="flex flex-col rounded-lg border bg-card">
        {group.items.map((item) => {
          const state = STATE[inboxState(item)]
          return (
            <li key={item.id} className="grid gap-x-4 gap-y-1 border-b px-4 py-2.5 last:border-b-0 sm:grid-cols-[9rem_minmax(0,1fr)_auto]">
              <Stamp at={item.at} timeZone={timeZone} />
              <div className="flex min-w-0 flex-col gap-1">
                <span className="flex flex-wrap items-center gap-2">
                  <Tag>{eventText(item.event)}</Tag>
                  {item.question && <span className="text-sm"><Words>{item.question.text}</Words></span>}
                </span>
                {item.text && <span className="text-sm text-muted-foreground"><Words>{item.text}</Words></span>}
              </div>
              <StatusBadge tone={state.tone} label={state.label} />
            </li>
          )
        })}
      </ol>
    </section>
  )
}

// the speaker's subscription inbox: its open questions to answer, then its updates by topic, newest first. read only
// while its tab shows
export function Inbox({ deviceId }: { deviceId: string }) {
  useLiveTopics(TOPICS)
  const api = useApi()
  const questionsRef = useRef<HTMLHeadingElement>(null)
  const inbox = useQuery(inboxQueryOptions(api, deviceId))

  if (inbox.isPending)
    return <LoadingLines lines={3} label="Loading the inbox" />
  if (inbox.isError) {
    return (
      <div className="rounded-lg border bg-card">
        <ErrorState title="The inbox could not be read" error={inbox.error} onRetry={() => void inbox.refetch()} />
      </div>
    )
  }

  const items = inbox.data.items
  const questions = items.filter(isOpenQuestion)
  const groups = groupByTopic(items)
  return (
    <div className="flex flex-col gap-8">
      <section aria-labelledby={`${deviceId}-questions`} className="flex flex-col gap-3">
        <h3 ref={questionsRef} id={`${deviceId}-questions`} tabIndex={-1} className="text-lg font-semibold outline-none">Open questions</h3>
        {questions.length === 0
          ? <p className="text-base text-muted-foreground">Nothing waits for an answer.</p>
          : (
              <ul className="flex flex-col gap-4">
                {questions.map(item => <OpenQuestion key={item.id} deviceId={deviceId} item={item} done={questionsRef} />)}
              </ul>
            )}
      </section>
      <section aria-labelledby={`${deviceId}-updates`} className="flex flex-col gap-4">
        <h3 id={`${deviceId}-updates`} className="text-lg font-semibold">Updates</h3>
        {groups.length === 0
          ? (
              <div className="rounded-lg border bg-card">
                <EmptyState
                  icon={InboxIcon}
                  title="Nothing in the inbox"
                  description="Updates of the topics this speaker follows show here until a turn on it reads them, and questions until they are answered."
                />
              </div>
            )
          : groups.map(group => <Group key={group.topic} group={group} />)}
      </section>
    </div>
  )
}
