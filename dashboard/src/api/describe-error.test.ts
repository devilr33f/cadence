import { expect, it } from 'vitest'
import { describeError } from './describe-error'
import { ApiError } from './error'

const refused = new ApiError(409, { error: 'command_failed', detail: 'no_session' })

it.each<[string, unknown, string]>([
  [
    'an ApiError',
    new ApiError(503, { error: 'unavailable' }),
    'The server answered 503 unavailable.',
  ],
  ['an ApiError with its detail', refused, 'The server answered 409 command_failed (no_session).'],
  // the ssr server serializes an error into the page without its class: the browser must word it the same
  [
    'its message alone',
    new Error(refused.message),
    'The server answered 409 command_failed (no_session).',
  ],
  [
    'its fields without the class',
    { name: 'ApiError', status: 503, error: 'unavailable' },
    'The server answered 503 unavailable.',
  ],
  [
    'a failed fetch',
    new TypeError('fetch failed'),
    'The server could not be reached.',
  ],
  [
    'a failed fetch without its class',
    { name: 'TypeError', message: 'Failed to fetch' },
    'The server could not be reached.',
  ],
  // the ssr server's fetch (undici) and each browser's own words for a request that got no answer, the class lost
  [
    'the ssr server\'s failed fetch without its class',
    new Error('fetch failed'),
    'The server could not be reached.',
  ],
  [
    'chrome\'s failed fetch without its class',
    { name: 'Error', message: 'Failed to fetch' },
    'The server could not be reached.',
  ],
  [
    'firefox\'s failed fetch without its class',
    new Error('NetworkError when attempting to fetch resource.'),
    'The server could not be reached.',
  ],
  [
    'safari\'s failed fetch without its class',
    new Error('Load failed'),
    'The server could not be reached.',
  ],
  // the ssr server's call that ran out of time (SSR_TIMEOUT_MS), with its class and as the page reads it without
  [
    'the ssr server\'s timed out call',
    new DOMException('The operation was aborted due to timeout', 'TimeoutError'),
    'The server did not answer in time.',
  ],
  [
    'the ssr server\'s timed out call without its class',
    new Error('The operation was aborted due to timeout'),
    'The server did not answer in time.',
  ],
  [
    'anything else',
    new Error('boom'),
    'Something went wrong in the page.',
  ],
  [
    'a message that only looks close',
    new Error('api 503 Service Unavailable'),
    'Something went wrong in the page.',
  ],
])('words %s in codes', (_, error, expected) => {
  expect(describeError(error)).toBe(expected)
})

it('words a bug in the page as one, not as the server being away', () => {
  expect(describeError(new TypeError('Cannot read properties of undefined (reading \'id\')'))).toBe('Something went wrong in the page.')
})
