// json's own number grammar, so what the field accepts is what the body can carry
const JSON_NUMBER = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:e[+-]?\d+)?$/i

export function parseNumber(text: string, kind: 'integer' | 'number'): { value?: number, error?: string } {
  const trimmed = text.trim()
  if (trimmed === '')
    return {}
  if (!JSON_NUMBER.test(trimmed)) {
    return {
      error: kind === 'integer'
        ? 'Must be a whole number.'
        : 'Must be a number.',
    }
  }
  const value = Number(trimmed)
  if (!Number.isFinite(value))
    return { error: 'Must be a number.' }
  if (kind === 'integer' && !Number.isInteger(value))
    return { error: 'Must be a whole number.' }
  return { value }
}
