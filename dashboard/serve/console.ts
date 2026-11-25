import type { Level, Log } from './log.ts'
import { errorClass } from './log.ts'

type Method = 'error' | 'warn' | 'info' | 'log' | 'debug' | 'trace'

const LEVELS: Record<Method, Level> = {
  error: 'ERROR',
  warn: 'WARN',
  info: 'INFO',
  log: 'INFO',
  debug: 'INFO',
  trace: 'INFO',
}

// react, the router and start log through console with whatever they hold (an error's message, a url and its query,
// a page's data); none of it reaches the log: a call is one line of its level and, given an error, that error's class
export function quietConsole(log: Log, target: Console = console) {
  for (const method of Object.keys(LEVELS) as Method[]) {
    target[method] = (...args: unknown[]) => {
      const error = args.find(arg => arg instanceof Error)
      log(LEVELS[method], 'console', error === undefined
        ? { method }
        : {
            method,
            error: errorClass(error),
          })
    }
  }
}
