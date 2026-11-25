import type { ModeView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { ModeAliases, ModelId } from './mode-cells'
import { INTERIM_LABELS, REASONING_LABELS, toolsText } from './mode-words'

export const MODE_COLUMNS: readonly Column<ModeView>[] = [
  {
    id: 'name',
    header: 'Name',
    sortValue: m => m.name,
    cell: m => <span className="font-mono text-xs font-medium">{m.name}</span>,
  },
  {
    id: 'model',
    header: 'Model',
    cell: m => m.model === ''
      ? <span className="text-muted-foreground">Device default</span>
      : <ModelId id={m.model} />,
    className: 'max-md:hidden',
  },
  {
    id: 'reasoning',
    header: 'Reasoning',
    cell: m => REASONING_LABELS[m.reasoning],
  },
  {
    id: 'steps',
    header: 'Steps',
    align: 'end',
    cell: m => m.maxSteps,
    className: 'max-lg:hidden',
  },
  {
    id: 'seconds',
    header: 'Seconds',
    align: 'end',
    cell: m => m.maxSeconds,
    className: 'max-lg:hidden',
  },
  {
    id: 'tokens',
    header: 'Tokens',
    align: 'end',
    cell: m => m.maxTokens ?? <span className="text-muted-foreground">Default</span>,
    className: 'max-lg:hidden',
  },
  {
    id: 'interim',
    header: 'Interim text',
    cell: m => INTERIM_LABELS[m.interim],
    className: 'max-xl:hidden',
  },
  {
    id: 'tools',
    header: 'Tools',
    cell: m => toolsText(m.tools),
    className: 'max-sm:hidden',
  },
  {
    id: 'aliases',
    header: 'Aliases',
    cell: m => <ModeAliases mode={m} />,
  },
]
