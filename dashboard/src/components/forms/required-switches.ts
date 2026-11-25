import type { FieldNode } from './json-schema'
import { isObject } from './json-pointer'

// a required switch left untouched shows off, and off is what the form sends: one press to turn it on, none for off;
// only inside objects the value has, so an absent object stays absent
export function withRequiredSwitches(node: FieldNode, value: unknown): unknown {
  if (node.kind !== 'object' || !isObject(value))
    return value
  let out = value
  for (const field of node.fields) {
    const current = value[field.name]
    const next = field.kind === 'boolean' && field.required && current === undefined
      ? false
      : withRequiredSwitches(field, current)
    if (next !== current)
      out = { ...out, [field.name]: next }
  }
  return out
}
