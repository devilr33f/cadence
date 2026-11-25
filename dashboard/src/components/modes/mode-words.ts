import type { Interim, Reasoning } from './mode-draft'

export const REASONING_LABELS: Record<Reasoning, string> = {
  none: 'None',
  low: 'Low',
  medium: 'Medium',
  high: 'High',
}

// what happens to text that comes with tool calls
export const INTERIM_LABELS: Record<Interim, string> = {
  drop: 'Dropped',
  speak: 'Spoken',
}

export function toolsText(tools: readonly string[] | null): string {
  if (tools === null)
    return 'All'
  if (tools.length === 0)
    return 'None'
  return tools.length === 1
    ? '1 tool'
    : `${tools.length} tools`
}
