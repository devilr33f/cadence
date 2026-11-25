// an event's data is the server's json: read each field for what it is, and nothing else
function field(data: unknown, key: string): unknown {
  return typeof data === 'object' && data !== null
    ? (data as Record<string, unknown>)[key]
    : undefined
}

export function text(data: unknown, key: string): string | undefined {
  const value = field(data, key)
  return typeof value === 'string'
    ? value
    : undefined
}

// a nested object, read as the others are
export function record(data: unknown, key: string): Record<string, unknown> | undefined {
  const value = field(data, key)
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

export function count(data: unknown, key: string): number | undefined {
  const value = field(data, key)
  return typeof value === 'number' && Number.isFinite(value)
    ? value
    : undefined
}

export function flag(data: unknown, key: string): boolean | undefined {
  const value = field(data, key)
  return typeof value === 'boolean'
    ? value
    : undefined
}

// tool arguments arrive as the model's json text, results as json: both shown indented
export function json(data: unknown, key: string): string | undefined {
  const value = field(data, key)
  if (value === undefined || value === null)
    return undefined
  if (typeof value === 'string') {
    try {
      return JSON.stringify(JSON.parse(value), null, 2)
    }
    catch {
      return value
    }
  }
  return JSON.stringify(value, null, 2)
}
