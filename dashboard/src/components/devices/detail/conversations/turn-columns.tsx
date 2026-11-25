import type { TurnView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/status-badge'
import { Muted, ToolsCell, TriggerCell, UserCell, Words } from './turn-cells'
import { cut, tokensText, turnStamp } from './turn-words'

export function turnColumns(timeZone: string | null): Column<TurnView>[] {
  return [
    {
      id: 'at',
      header: 'When',
      cell: turn => <time dateTime={turn.at} className="font-semibold">{turnStamp(turn, timeZone)}</time>,
    },
    {
      id: 'trigger',
      header: 'Trigger',
      cell: turn => (
        <div className="flex flex-col items-start gap-1 py-1">
          <TriggerCell trigger={turn.trigger} />
          {turn.failure && <StatusBadge tone="danger" label="Failed" detail={turn.failure.code} />}
        </div>
      ),
    },
    {
      id: 'mode',
      header: 'Mode',
      cell: turn => turn.mode
        ? <span className="font-mono text-xs">{turn.mode}</span>
        : <Muted>None</Muted>,
      className: 'max-lg:hidden',
    },
    {
      id: 'user',
      header: 'User',
      cell: turn => <UserCell turn={turn} />,
      className: 'py-2 whitespace-normal',
    },
    {
      id: 'reply',
      header: 'Reply',
      cell: turn => turn.reply
        ? <Words>{cut(turn.reply)}</Words>
        : <Muted>—</Muted>,
      className: 'py-2 whitespace-normal max-sm:hidden',
    },
    {
      id: 'tools',
      header: 'Tools',
      cell: turn => <ToolsCell tools={turn.tools} />,
      className: 'py-2 whitespace-normal max-md:hidden',
    },
    {
      id: 'tokens',
      header: 'Tokens',
      align: 'end',
      cell: turn => turn.tokens
        ? tokensText(turn.tokens)
        : <Muted>None</Muted>,
      className: 'max-xl:hidden',
    },
  ]
}
