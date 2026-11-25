import type { JsonObject } from './json-pointer'
import { isObject } from './json-pointer'

export type Parsed<T> = { value: T } | { error: string }

// the text of a json editor, with the reason it is held back
export function parseJson(text: string): Parsed<unknown> {
  try {
    return {
      value: JSON.parse(text) as unknown,
    }
  }
  catch {
    return { error: 'Not valid JSON.' }
  }
}

// a whole document (a config, a command's arguments) must be an object
export function parseJsonObject(text: string): Parsed<JsonObject> {
  const parsed = parseJson(text)
  if ('error' in parsed)
    return parsed
  return isObject(parsed.value)
    ? { value: parsed.value }
    : { error: 'Must be a JSON object.' }
}
