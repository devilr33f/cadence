export const BACKOFF_MIN_MS = 1000
export const BACKOFF_MAX_MS = 30_000

// 1 s doubling to 30 s, each wait spread by ±25 % and held inside those bounds
export function backoffDelay(attempt: number, random: () => number): number {
  const base = Math.min(BACKOFF_MAX_MS, BACKOFF_MIN_MS * 2 ** attempt)
  const jittered = Math.round(base * (0.75 + 0.5 * random()))
  return Math.min(BACKOFF_MAX_MS, Math.max(BACKOFF_MIN_MS, jittered))
}
