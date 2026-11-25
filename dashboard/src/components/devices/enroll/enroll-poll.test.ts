import type { ReadState } from './enroll-poll'
import type { Detail } from '@/api/gen'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/api/error'
import { expiredUnused, LATE_MS, POLL_MS, pollMs } from './enroll-poll'

const EXPIRES = Date.parse('2026-11-12T15:10:00Z')

afterEach(() => {
  vi.useRealTimers()
})

function speaker(status: string, online = false): Detail {
  return {
    id: 'd',
    name: 'Kitchen',
    model: 'sim',
    room: '',
    status,
    online,
    daemonVersion: '',
    lastSeenAt: null,
    capabilities: [],
  }
}

function read(data: Detail | undefined, at: number, error: unknown = null): ReadState {
  return {
    data,
    error,
    dataUpdatedAt: data ? at : 0,
    errorUpdatedAt: error ? at : 0,
  }
}

// the page's clock, which a render reads the poll at
function at(time: number) {
  vi.useFakeTimers({ now: time })
}

describe('the enroll poll', () => {
  it('reads every 2 s while the code is valid', () => {
    at(EXPIRES - 4000)
    expect(pollMs(read(speaker('pending'), EXPIRES - 5000), EXPIRES)).toBe(POLL_MS)
  })

  // the speaker may have used the code after the last read: a render after the expiry must not end the poll on it
  it('reads again after the expiry when the last read came before it', () => {
    at(EXPIRES + 100)
    expect(pollMs(read(speaker('pending'), EXPIRES - 500), EXPIRES)).toBe(POLL_MS)
    expect(expiredUnused(read(speaker('pending'), EXPIRES - 500), EXPIRES)).toBe(false)
  })

  // the server's clock, which the expiry is in, may run behind the page's
  it('reads again when the last read came within a poll of the expiry', () => {
    at(EXPIRES + LATE_MS)
    expect(pollMs(read(speaker('pending'), EXPIRES + LATE_MS - 1), EXPIRES)).toBe(POLL_MS)
  })

  it('stops once a read well after the expiry says the speaker never enrolled', () => {
    at(EXPIRES + LATE_MS)
    const late = read(speaker('pending'), EXPIRES + LATE_MS)
    expect(pollMs(late, EXPIRES)).toBe(false)
    expect(expiredUnused(late, EXPIRES)).toBe(true)
  })

  it('counts a read that failed well after the expiry as well, the code being unusable either way', () => {
    at(EXPIRES + LATE_MS)
    const failed = new ApiError(503, { error: 'unavailable' })
    expect(pollMs({
      ...read(speaker('pending'), EXPIRES - 500),
      error: failed,
      errorUpdatedAt: EXPIRES + 100,
    }, EXPIRES)).toBe(POLL_MS)
    expect(pollMs({
      ...read(speaker('pending'), EXPIRES - 500),
      error: failed,
      errorUpdatedAt: EXPIRES + LATE_MS,
    }, EXPIRES)).toBe(false)
  })

  it('follows a speaker that enrolled until it is online, the code\'s expiry no matter', () => {
    at(EXPIRES + 60_000)
    expect(pollMs(read(speaker('enrolled'), EXPIRES + 60_000), EXPIRES)).toBe(POLL_MS)
    expect(expiredUnused(read(speaker('enrolled'), EXPIRES + 60_000), EXPIRES)).toBe(false)
    expect(pollMs(read(speaker('enrolled', true), EXPIRES + 60_000), EXPIRES)).toBe(false)
  })

  it('stops on a refusal, which will not change', () => {
    at(EXPIRES - 4000)
    expect(pollMs(read(undefined, EXPIRES - 5000, new ApiError(403, { error: 'forbidden' })), EXPIRES)).toBe(false)
    expect(pollMs(read(undefined, EXPIRES - 5000, new ApiError(503, { error: 'unavailable' })), EXPIRES)).toBe(POLL_MS)
  })
})
