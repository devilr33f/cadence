import type { KeyValue } from '@/components/data/key-value-list'

function scalar(value: unknown): string | undefined {
  switch (typeof value) {
    case 'string':
      return value === ''
        ? '""'
        : value
    case 'number':
    case 'boolean':
      return String(value)
    default:
      return value === null
        ? 'null'
        : undefined
  }
}

// a json object's top-level keys as a key-value list: scalars as text, anything nested as compact json
export function jsonItems(value: Record<string, unknown>): KeyValue[] {
  return Object.entries(value).map(([label, v]) => {
    const text = scalar(v)
    return text === undefined
      ? {
          label,
          value: JSON.stringify(v),
          mono: true,
        }
      : { label, value: text }
  })
}
