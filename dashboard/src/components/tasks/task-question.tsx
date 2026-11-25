import type { RefObject } from 'react'
import type { Question } from '@/lib/tasks'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { Button } from '@/components/ui/button'
import { useTaskActions } from '@/lib/tasks'
import { TaskReply } from './task-reply'

function QuestionText({ text }: { text: string }) {
  return <span className="break-words whitespace-pre-wrap">{text}</span>
}

// an approval is answered once or denied, each after asking: the server keeps the answer with via: dashboard. each
// dialog answers the question it opened on (its token, its text); a newer one read under it blocks the confirm
function Approval({ taskId, question, done }: { taskId: string, question: Question, done: RefObject<HTMLElement | null> }) {
  const actions = useTaskActions(taskId)
  return (
    <div className="flex flex-wrap gap-2">
      <ConfirmDialog
        trigger={<Button type="button">Approve once</Button>}
        title="Approve this once?"
        description={<QuestionText text={question.text} />}
        confirmLabel="Approve once"
        inlineError
        finalFocus={done}
        version={question.token}
        onConfirm={opened => actions.approve('once', opened ?? question.token)}
      />
      <ConfirmDialog
        trigger={<Button type="button" variant="outline">Deny</Button>}
        title="Deny this request?"
        description={<QuestionText text={question.text} />}
        confirmLabel="Deny the request"
        tone="destructive"
        inlineError
        finalFocus={done}
        version={question.token}
        onConfirm={opened => actions.approve('deny', opened ?? question.token)}
      />
    </div>
  )
}

// what the task asks the owner: a clarify question takes a reply, an approval a decision
export function TaskQuestion({ taskId, question, done }: { taskId: string, question: Question, done: RefObject<HTMLElement | null> }) {
  return (
    <div className="flex flex-col gap-4 rounded-lg border bg-card p-5">
      <p className="text-base"><QuestionText text={question.text} /></p>
      {question.kind === 'approval'
        ? <Approval taskId={taskId} question={question} done={done} />
        // a reply belongs to its question: a new one (a new token) starts an empty form, so a draft never goes out with
        // another question's token
        : <TaskReply key={question.token} taskId={taskId} question={question} done={done} />}
    </div>
  )
}
