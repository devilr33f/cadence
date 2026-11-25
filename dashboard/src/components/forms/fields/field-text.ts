import type { FieldNode } from '../json-schema'

export function fieldLabel(node: FieldNode): string {
  return typeof node.schema.title === 'string'
    ? node.schema.title
    : node.name
}

// what a control says about itself: its description (`${id}-description`), then why it is wrong (`${id}-error`)
export function describedBy(id: string, { description = false, error }: { description?: boolean, error?: string }): string | undefined {
  const ids = [
    description && `${id}-description`,
    error && `${id}-error`,
  ].filter(Boolean)
  return ids.length > 0
    ? ids.join(' ')
    : undefined
}

// a schema field's: its schema's description, then its error
export function fieldDescribedBy(node: FieldNode, id: string, error?: string): string | undefined {
  return describedBy(id, { description: typeof node.schema.description === 'string', error })
}

export function defaultHint(node: FieldNode): string | undefined {
  const value = node.schema.default
  return value === undefined
    ? undefined
    : `Default: ${typeof value === 'string'
      ? value
      : JSON.stringify(value)}`
}

export function prettyJson(value: unknown): string {
  return value === undefined
    ? ''
    : JSON.stringify(value, null, 2)
}

// a list's text typed but never added would be lost on submit: it holds the submit back
export const UNADDED_ITEM = 'Add the typed item, or clear it.'
