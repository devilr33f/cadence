import type { SkillView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'

// where a skill comes from, as the owner reads it
export function sourceText(skill: SkillView): string {
  if (skill.source === 'builtin')
    return 'Built-in'
  return skill.overrides
    ? 'Yours, replaces built-in'
    : 'Yours'
}

export function modesText(modes: readonly string[] | null): string {
  if (modes === null)
    return 'All'
  return modes.length === 0
    ? 'None'
    : modes.join(', ')
}

export const SKILL_COLUMNS: readonly Column<SkillView>[] = [
  {
    id: 'name',
    header: 'Name',
    sortValue: s => s.name,
    cell: s => <span className="font-mono text-xs font-medium">{s.name}</span>,
  },
  {
    id: 'state',
    header: 'State',
    cell: s => s.enabled
      ? 'On'
      : <span className="text-muted-foreground">Off</span>,
  },
  {
    id: 'source',
    header: 'Source',
    cell: sourceText,
    className: 'max-sm:hidden',
  },
  {
    id: 'description',
    header: 'Description',
    cell: s => <span className="line-clamp-2 min-w-48 whitespace-normal">{s.description}</span>,
  },
  {
    id: 'triggers',
    header: 'Triggers',
    align: 'end',
    cell: s => s.triggers.length,
    className: 'max-lg:hidden',
  },
  {
    id: 'modes',
    header: 'Modes',
    cell: s => <span className="font-mono text-xs">{modesText(s.modes)}</span>,
    className: 'max-md:hidden',
  },
]
