import type { MemoryJson } from '@/api/gen'
import { Tag } from '@/components/data/tag'
import { scopeName } from './memory-words'

// what the assistant remembers, as text: never markup, wrapped within its column
export function MemoryText({ text }: { text: string }) {
  return <span className="block max-w-[60ch] min-w-48 break-words whitespace-pre-wrap">{text}</span>
}

export function ScopeTag({ memory, names }: { memory: Pick<MemoryJson, 'scope' | 'deviceId'>, names: ReadonlyMap<string, string> }) {
  const known = memory.scope !== 'device' || (memory.deviceId !== null && names.has(memory.deviceId))
  return <Tag mono={!known}>{scopeName(memory, names)}</Tag>
}
