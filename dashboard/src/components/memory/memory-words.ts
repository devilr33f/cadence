import type { MemoryJson } from '@/api/gen'

const KIND_LABELS: Readonly<Record<string, string>> = {
  preference: 'Preference',
  fact: 'Fact',
  alias: 'Alias',
  lesson: 'Lesson',
}

// a kind newer than the page shows as the server names it
export function kindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind
}

const SHORT = 48

// a memory named in a button's label, so each row's buttons say which memory they act on
export function short(text: string): string {
  const chars = [...text]
  return chars.length > SHORT
    ? `${chars.slice(0, SHORT - 1).join('')}…`
    : text
}

// the scope a memory belongs to: the household, or a speaker by its name (its id while the name is unknown)
export function scopeName(memory: Pick<MemoryJson, 'scope' | 'deviceId'>, names: ReadonlyMap<string, string>): string {
  if (memory.scope !== 'device' || !memory.deviceId)
    return 'Global'
  return names.get(memory.deviceId) ?? memory.deviceId
}

export type SuggestionChange = 'new' | 'update' | 'delete'

// a pending memory is new; a proposal names the active one it would update or delete
export function changeOf(memory: MemoryJson): SuggestionChange {
  if (memory.proposal === 'delete')
    return 'delete'
  return memory.replaces
    ? 'update'
    : 'new'
}

export const CHANGE_LABELS: Readonly<Record<SuggestionChange, string>> = {
  new: 'New',
  update: 'Update',
  delete: 'Delete',
}
