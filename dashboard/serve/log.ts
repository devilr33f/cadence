export type Level = 'INFO' | 'WARN' | 'ERROR'
export type Fields = Record<string, string | number | boolean>
export type Log = (level: Level, msg: string, fields?: Fields) => void

// one json line per event, as the server's own log: events, codes, paths and numbers only (rule 8)
export function createLog(write: (line: string) => void, now: () => Date = () => new Date()): Log {
  return (level, msg, fields = {}) => write(`${JSON.stringify({
    time: now().toISOString(),
    level,
    msg,
    ...fields,
  })}\n`)
}

const CLASS = /^[A-Z][A-Za-z]{0,39}$/

// what an error may say of itself in a log: its class, when that is a plain name; never its message
export function errorClass(error: unknown): string {
  const name = error instanceof Error
    ? error.name
    : ''
  return CLASS.test(name)
    ? name
    : 'Error'
}
