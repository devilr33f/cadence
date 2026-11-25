import type { ReactNode } from 'react'
import type { TurnCall, TurnMessage } from '@/api/gen'
import { useId } from 'react'
import { toolCallSummary, toolResultView } from './tool-card-registry'
import { SERVER_OWN } from './turn-words'

const ROLES: Readonly<Record<string, string>> = {
  user: 'User',
  assistant: 'Assistant',
  tool: 'Tool result',
}

// the turn's calls by id, so a result card reads the call it answers
interface Called {
  name: string
  args?: unknown
}

// arguments as the model sent them, laid out; a string stays as it came
function argumentsText(call: TurnCall): string {
  if (call.arguments === undefined)
    return ''
  return typeof call.arguments === 'string'
    ? call.arguments
    : JSON.stringify(call.arguments, null, 2)
}

function skillName(call: TurnCall): string {
  if (call.arguments !== undefined && typeof call.arguments !== 'string') {
    const name = (call.arguments as { name?: unknown }).name
    if (typeof name === 'string' && name.length > 0)
      return name
  }
  return 'unknown'
}

// tool data as stored, set as preformatted text in a figure named by its caption: never markup
function Block({ caption, children }: { caption: ReactNode, children: string }) {
  const captionId = useId()
  return (
    <figure aria-labelledby={captionId} className="flex flex-col gap-1">
      <figcaption id={captionId} className="text-xs text-muted-foreground">{caption}</figcaption>
      <pre className="max-h-96 overflow-auto rounded-md border bg-muted p-3 font-mono text-xs whitespace-pre-wrap wrap-anywhere">{children}</pre>
    </figure>
  )
}

function ToolResult({ message, called }: { message: TurnMessage, called?: string }) {
  return (
    <Block caption={(
      <>
        {called
          ? (
              <>
                {'Result of '}
                <code className="font-mono">{called}</code>
              </>
            )
          : 'Result'}
        {message.code && (
          <>
            {' '}
            <code data-tone="danger" className="font-mono text-destructive">{message.code}</code>
          </>
        )}
      </>
    )}
    >
      {message.content}
    </Block>
  )
}

function Message({ message, event, calls }: { message: TurnMessage, event: boolean, calls: ReadonlyMap<string, Called> }) {
  const known = Object.hasOwn(ROLES, message.role)
  const label = message.role === 'user' && event
    ? SERVER_OWN
    : known ? ROLES[message.role] : message.role
  const called = message.toolCallId !== undefined
    ? calls.get(message.toolCallId)
    : undefined
  // a result the cards know draws as one, the stored json behind its disclosure; the
  // rest stay the raw figures they were
  const card = message.role === 'tool' && called !== undefined
    ? toolResultView({ name: called.name, message, args: called.args })
    : null
  return (
    <li className="flex flex-col gap-1.5">
      <p className={known ? 'text-xs font-medium text-muted-foreground' : 'font-mono text-xs text-muted-foreground'}>{label}</p>
      {message.role === 'tool'
        ? called !== undefined && called.name === 'load_skill'
          ? (
              <details className="text-xs text-muted-foreground">
                <summary className="cursor-pointer">Raw skill result</summary>
                <ToolResult message={message} called={called.name} />
              </details>
            )
          : card !== null
            ? card
            : <ToolResult message={message} called={called?.name ?? message.toolCallId} />
        : message.content !== '' && <p className="break-words whitespace-pre-wrap">{message.content}</p>}
      {message.toolCalls?.map((call) => {
        if (call.name === 'load_skill') {
          return (
            <div key={call.id} className="flex items-center gap-2 text-xs text-muted-foreground">
              <span aria-hidden="true">✦</span>
              <span>
                Loaded skill:
                {' '}
                <code className="font-mono">{skillName(call)}</code>
              </span>
            </div>
          )
        }
        const summary = toolCallSummary({ call })
        return summary !== null
          ? <div key={call.id}>{summary}</div>
          : (
              <Block
                key={call.id}
                caption={(
                  <>
                    {'Tool call '}
                    <code className="font-mono">{call.name}</code>
                  </>
                )}
              >
                {argumentsText(call)}
              </Block>
            )
      })}
    </li>
  )
}

// a turn's messages in their stored order; an event turn's user message is the server's framing
export function TurnMessages({ messages, event }: { messages: readonly TurnMessage[], event: boolean }) {
  const calls = new Map(messages.flatMap(m => m.toolCalls ?? []).map(call => [
    call.id,
    { name: call.name, args: call.arguments },
  ]))
  const ordered = [...messages].sort((a, b) => a.seq - b.seq)
  return (
    <ol aria-label="Messages" className="flex flex-col gap-5">
      {ordered.map(message => <Message key={message.seq} message={message} event={event} calls={calls} />)}
    </ol>
  )
}
