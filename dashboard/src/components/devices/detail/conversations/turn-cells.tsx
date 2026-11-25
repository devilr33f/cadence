import type { ReactNode } from 'react'
import type { TurnTool, TurnView } from '@/api/gen'
import { Tag } from '@/components/data/tag'
import { cut, isEventTurn, SERVER_OWN, triggerText, UNRECORDED } from './turn-words'

export function Muted({ children }: { children: ReactNode }) {
  return <span className="text-muted-foreground">{children}</span>
}

// transcripts and replies are text, never markup, and wrap in their cell
export function Words({ children }: { children: string }) {
  return <span className="block max-w-[48ch] min-w-48 break-words">{children}</span>
}

export function TriggerCell({ trigger }: { trigger: string | undefined }) {
  const label = triggerText(trigger)
  if (label)
    return label
  return trigger
    ? <span className="font-mono text-xs">{trigger}</span>
    : <Muted>{UNRECORDED}</Muted>
}

export function UserCell({ turn }: { turn: TurnView }) {
  if (!turn.user)
    return <Muted>None</Muted>
  return (
    <span className="flex max-w-[48ch] min-w-48 flex-col items-start gap-1">
      {isEventTurn(turn) && <Tag>{SERVER_OWN}</Tag>}
      <span className="break-words">{cut(turn.user)}</span>
    </span>
  )
}

// each call by name, in order, with the error code its result carried
export function ToolsCell({ tools }: { tools: readonly TurnTool[] }) {
  if (tools.length === 0)
    return <Muted>None</Muted>
  return (
    <ul className="flex max-w-64 flex-wrap gap-1">
      {tools.map((tool, i) => (
        // eslint-disable-next-line react/no-array-index-key -- a turn may call one tool twice
        <li key={`${i}-${tool.name}`}>
          <Tag mono className="gap-1.5">
            {tool.name}
            {tool.code && (
              <>
                {' '}
                <span data-tone="danger" className="text-destructive">{tool.code}</span>
              </>
            )}
          </Tag>
        </li>
      ))}
    </ul>
  )
}
