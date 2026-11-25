import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'

// the reply route's refusals (deploy/server/README.md, Subscriptions), each worded with its code
const REPLY_CODES: Readonly<Record<string, string>> = {
  not_found: 'This question is no longer open: it was answered or withdrawn',
  answer_mismatch: 'The answer does not fit the question as it is now',
  module_unavailable: 'The system that asked is not connected',
  module_failed: 'The system that asked did not take the answer',
}

export function replyRefusal(error: unknown): string {
  if (!(error instanceof ApiError))
    return describeError(error)
  const text = REPLY_CODES[error.error]
  return text === undefined
    ? describeError(error)
    : `${text} (${error.status} ${error.error}).`
}
