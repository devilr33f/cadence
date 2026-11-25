import { ApiError } from './error'

interface Answer {
  status: number
  error: string
  detail?: string
}

// ApiError's message: `api <status> <code>[ <detail>]`, codes and details being snake_case tokens
const MESSAGE = /^api (\d{3}) ([a-z0-9_]+)(?: ([a-z0-9_]+))?$/

// the ssr server serializes an error into the page without its class (a plain Error, maybe with its fields), so the
// browser reads the same answer from the fields or the message and renders the same text
function answerOf(error: unknown): Answer | undefined {
  if (error instanceof ApiError)
    return error
  if (typeof error !== 'object' || error === null)
    return undefined
  const { status, error: code, detail, message } = error as Record<string, unknown>
  if (typeof status === 'number' && typeof code === 'string') {
    return {
      status,
      error: code,
      detail: typeof detail === 'string'
        ? detail
        : undefined,
    }
  }
  const parts = typeof message === 'string'
    ? MESSAGE.exec(message)
    : null
  return parts
    ? {
        status: Number(parts[1]),
        error: parts[2]!,
        detail: parts[3],
      }
    : undefined
}

// a fetch that got no answer: undici on the ssr server, then chrome, firefox and safari. fetch throws a TypeError with
// one of these, and the ssr server's error reaches the page as a plain Error, so the message decides either way: any
// other TypeError is a bug in the page, not the server being away
const FAILED_FETCH_MESSAGES: ReadonlySet<string> = new Set([
  'fetch failed',
  'Failed to fetch',
  'NetworkError when attempting to fetch resource.',
  'Load failed',
])

function isFailedFetch(error: unknown): boolean {
  if (typeof error !== 'object' || error === null)
    return false
  const { message } = error as Record<string, unknown>
  return typeof message === 'string' && FAILED_FETCH_MESSAGES.has(message)
}

// the ssr server's call that ran out of time (api/server.ts): its abort reason, by its class or as the page reads it
const TIMED_OUT_MESSAGE = 'The operation was aborted due to timeout'

function isTimedOut(error: unknown): boolean {
  if (typeof error !== 'object' || error === null)
    return false
  const { name, message } = error as Record<string, unknown>
  return name === 'TimeoutError' || message === TIMED_OUT_MESSAGE
}

// codes only, as the api gives them; never a body or a value
export function describeError(error: unknown): string {
  const answer = answerOf(error)
  if (answer) {
    return answer.detail
      ? `The server answered ${answer.status} ${answer.error} (${answer.detail}).`
      : `The server answered ${answer.status} ${answer.error}.`
  }
  if (isFailedFetch(error))
    return 'The server could not be reached.'
  if (isTimedOut(error))
    return 'The server did not answer in time.'
  return 'Something went wrong in the page.'
}
