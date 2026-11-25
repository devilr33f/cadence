import type { ErrorBody } from './gen'

// the code of an error answer whose body is no ErrorBody, such as a proxy's own page
export const UNEXPECTED_RESPONSE = 'unexpected_response'

const DETAIL_TOKEN = /^[a-z0-9_]{1,64}$/

export class ApiError extends Error {
  readonly status: number
  readonly error: string
  readonly path?: string
  readonly detail?: string
  // seconds to wait, from a 429's Retry-After
  readonly retryAfter?: number

  // the message holds codes only (a detail joins it only as a token, as M7a-R1 promises), so it names the answer
  // wherever the class is lost, such as an error the ssr server serialized into the page
  constructor(status: number, body: ErrorBody, retryAfter?: number) {
    super(body.detail && DETAIL_TOKEN.test(body.detail)
      ? `api ${status} ${body.error} ${body.detail}`
      : `api ${status} ${body.error}`)
    this.name = 'ApiError'
    this.status = status
    this.error = body.error
    this.path = body.path
    this.detail = body.detail
    this.retryAfter = retryAfter
  }
}

function isErrorBody(body: unknown): body is ErrorBody {
  if (typeof body !== 'object' || body === null)
    return false
  const { error, path, detail } = body as Record<string, unknown>
  return typeof error === 'string'
    && (path === undefined || typeof path === 'string')
    && (detail === undefined || typeof detail === 'string')
}

export function toApiError(status: number, body: unknown, retryAfter?: number): ApiError {
  return new ApiError(status, isErrorBody(body)
    ? body
    : { error: UNEXPECTED_RESPONSE }, retryAfter)
}

// the server sends whole seconds; anything else is no wait the page can name
export function retryAfterSeconds(value: string | null): number | undefined {
  if (value === null || !/^\d+$/.test(value.trim()))
    return undefined
  return Number(value.trim())
}
