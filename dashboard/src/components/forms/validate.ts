import type { FieldNode, ScalarNode } from './json-schema'
import { isObject } from './json-pointer'

function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1
    ? ''
    : 's'}`
}

// a scalar's own problem, or undefined; the schema's keywords as the form reads them (json-schema.ts)
export function scalarError(node: ScalarNode, value: unknown): string | undefined {
  const s = node.schema
  switch (node.kind) {
    case 'enum':
      return node.options.includes(value as string)
        ? undefined
        : 'Choose one of the options.'
    case 'string': {
      if (typeof value !== 'string')
        return 'Must be text.'
      const length = [...value].length
      if (typeof s.minLength === 'number' && length < s.minLength)
        return `At least ${plural(s.minLength, 'character')}.`
      if (typeof s.maxLength === 'number' && length > s.maxLength)
        return `At most ${plural(s.maxLength, 'character')}.`
      if (typeof s.pattern === 'string' && !new RegExp(s.pattern, 'u').test(value))
        return `Must match ${s.pattern}.`
      return undefined
    }
    default: {
      if (typeof value !== 'number' || !Number.isFinite(value))
        return 'Must be a number.'
      if (node.kind === 'integer' && !Number.isInteger(value))
        return 'Must be a whole number.'
      if (typeof s.minimum === 'number' && value < s.minimum)
        return `At least ${s.minimum}.`
      if (typeof s.maximum === 'number' && value > s.maximum)
        return `At most ${s.maximum}.`
      if (typeof s.exclusiveMinimum === 'number' && value <= s.exclusiveMinimum)
        return `More than ${s.exclusiveMinimum}.`
      if (typeof s.exclusiveMaximum === 'number' && value >= s.exclusiveMaximum)
        return `Less than ${s.exclusiveMaximum}.`
      return undefined
    }
  }
}

function listError(node: Extract<FieldNode, { kind: 'tags' }>, value: unknown[]): string | undefined {
  const s = node.schema
  if (typeof s.minItems === 'number' && value.length < s.minItems)
    return `At least ${plural(s.minItems, 'item')}.`
  if (typeof s.maxItems === 'number' && value.length > s.maxItems)
    return `At most ${plural(s.maxItems, 'item')}.`
  if (s.uniqueItems === true && new Set(value).size !== value.length)
    return 'Each item only once.'
  for (const [i, item] of value.entries()) {
    const error = scalarError(node.item, item)
    if (error)
      return `Item ${i + 1}: ${error}`
  }
  return undefined
}

function check(node: FieldNode, value: unknown, errors: Map<string, string>) {
  if (value === undefined) {
    if (node.required)
      errors.set(node.pointer, 'Required.')
    return
  }
  let error: string | undefined
  switch (node.kind) {
    case 'object':
      if (isObject(value)) {
        for (const field of node.fields)
          check(field, value[field.name], errors)
      }
      return
    // the server's validation is the authority over a raw part; the editor checks only that it is json
    case 'raw':
      return
    case 'boolean':
      error = typeof value === 'boolean'
        ? undefined
        : 'Must be on or off.'
      break
    case 'tags':
      error = Array.isArray(value)
        ? listError(node, value)
        : 'Must be a list.'
      break
    default:
      error = scalarError(node, value)
  }
  if (error)
    errors.set(node.pointer, error)
}

// each field's problem, by its pointer, as the schema states it; the server's 422 stays the authority
export function validateForm(root: FieldNode, value: unknown): Map<string, string> {
  const errors = new Map<string, string>()
  check(root, value, errors)
  return errors
}
