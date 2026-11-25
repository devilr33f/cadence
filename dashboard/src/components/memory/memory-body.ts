import type { CreateMemoryRequest, EditMemoryRequest, MemoryJson } from '@/api/gen'
import type { MemoryKind } from '@/lib/memory'

// the scope select's value for the household
export const GLOBAL = 'global'

export interface MemoryValues {
  text: string
  kind: MemoryKind
  // GLOBAL, or the id of the speaker the memory belongs to
  scope: string
  pinned: boolean
}

export function valuesOf(memory?: MemoryJson): MemoryValues {
  return {
    text: memory?.text ?? '',
    kind: (memory?.kind ?? 'fact') as MemoryKind,
    scope: memory?.scope === 'device' && memory.deviceId
      ? memory.deviceId
      : GLOBAL,
    pinned: memory?.pinned ?? false,
  }
}

function scopeOf(scope: string): Pick<CreateMemoryRequest, 'scope' | 'deviceId'> {
  return scope === GLOBAL
    ? { scope: 'global' }
    : { scope: 'device', deviceId: scope }
}

export function createBody(values: MemoryValues): CreateMemoryRequest {
  return {
    ...scopeOf(values.scope),
    kind: values.kind,
    text: values.text,
  }
}

// only what changed: the server makes a learned memory manual once its text, kind or scope is edited, and a scope
// travels with its device
export function editBody(memory: MemoryJson, values: MemoryValues): EditMemoryRequest {
  const was = valuesOf(memory)
  return {
    ...(values.text !== was.text && { text: values.text }),
    ...(values.kind !== was.kind && { kind: values.kind }),
    ...(values.scope !== was.scope && scopeOf(values.scope)),
    ...(values.pinned !== was.pinned && { pinned: values.pinned }),
  }
}
