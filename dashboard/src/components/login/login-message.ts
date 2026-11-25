import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'

export function loginMessage(error: unknown): string {
  if (error instanceof ApiError && error.status === 401)
    return 'Wrong password.'
  if (error instanceof ApiError && error.status === 429) {
    return error.retryAfter === undefined
      ? 'Too many attempts, try again later.'
      : `Too many attempts, try again in ${error.retryAfter} s.`
  }
  return describeError(error)
}
