import type { Detail } from '@/api/gen'
import { ApiError } from '@/api/error'

export const POLL_MS = 2000
// how long after the expiry a read must come to settle that the code went unused: the speaker may have used it after
// the last read, and the server's clock, which the expiry is in, may run behind the page's
export const LATE_MS = 2000

// what the poll decides on: the device query's last answer and when it came
export interface ReadState {
  data?: Detail
  error: unknown
  dataUpdatedAt: number
  errorUpdatedAt: number
}

export function isOnline(detail?: Detail): boolean {
  return detail?.status === 'enrolled' && detail.online
}

// the code expired and a read made well after it found the speaker not enrolled: it never can be now
export function expiredUnused(state: ReadState, expiresAt: number): boolean {
  return state.data?.status !== 'enrolled'
    && Math.max(state.dataUpdatedAt, state.errorUpdatedAt) >= expiresAt + LATE_MS
}

// read every 2 s until the speaker is online; not after a refusal (it will not change), nor once the code expired
// unused. decided on the reads alone, never on the clock, so a render after the expiry cannot end the poll on a read
// that came before it
export function pollMs(state: ReadState, expiresAt: number): number | false {
  if (isOnline(state.data) || (state.error instanceof ApiError && state.error.status < 500))
    return false
  return expiredUnused(state, expiresAt)
    ? false
    : POLL_MS
}
