// json pointers (rfc 6901), the server's 422 path among them
export function pointerOf(path: readonly string[]): string {
  return path.map(segment => `/${segment.replaceAll('~', '~0').replaceAll('/', '~1')}`).join('')
}

export function segmentsOf(pointer: string): string[] {
  if (pointer === '')
    return []
  return pointer.slice(1).split('/').map(segment => segment.replaceAll('~1', '/').replaceAll('~0', '~'))
}

export type JsonObject = Record<string, unknown>

export function isObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function valueAt(root: unknown, path: readonly string[]): unknown {
  let at = root
  for (const segment of path) {
    if (!isObject(at))
      return undefined
    at = at[segment]
  }
  return at
}

// a copy of root with the value at path replaced (undefined removes the key), making the objects on the way
export function withValueAt(root: unknown, path: readonly string[], value: unknown): unknown {
  if (path.length === 0)
    return value
  const [head, ...rest] = path as [string, ...string[]]
  const object: JsonObject = isObject(root)
    ? { ...root }
    : {}
  const next = withValueAt(object[head], rest, value)
  if (next === undefined)
    delete object[head]
  else
    object[head] = next
  return object
}
