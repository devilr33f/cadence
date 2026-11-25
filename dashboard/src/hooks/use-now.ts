import { useSyncExternalStore } from 'react'

// one clock for the tab, ticking each second while any component reads it
const TICK_MS = 1000
const listeners = new Set<() => void>()
let now = Date.now()
let timer: ReturnType<typeof setInterval> | undefined

function subscribe(listener: () => void) {
  listeners.add(listener)
  if (timer === undefined) {
    timer = setInterval(() => {
      now = Date.now()
      listeners.forEach(l => l())
    }, TICK_MS)
  }
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0) {
      clearInterval(timer)
      timer = undefined
    }
  }
}

// while nothing ticks the clock stands still, so the first reader after a pause reads it afresh; a read within a tick of
// the last one gives the same value, as react requires of a snapshot
function snapshot() {
  if (timer === undefined && Math.abs(Date.now() - now) >= TICK_MS)
    now = Date.now()
  return now
}

const onServer = () => null

const still = () => () => {}

// the time, ticking: null on the ssr server and while the browser hydrates, so what it words never differs between the
// two. while not `ticking` it is read at each render but runs no clock (a countdown that has nothing to count)
export function useNow(ticking = true): number | null {
  return useSyncExternalStore(ticking ? subscribe : still, snapshot, onServer)
}

// whole seconds left until a moment, counted down each second while there is one; 0 once it passed, or without one
export function useSecondsLeft(until: number | undefined): number {
  const now = useNow(until !== undefined)
  if (until === undefined || now === null)
    return 0
  return Math.max(0, Math.ceil((until - now) / 1000))
}
