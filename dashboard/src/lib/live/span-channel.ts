import type { ResyncCause } from './feed'
import type { LiveEvent } from './frames'

// what a traces page hears: each span event and the feed's span_dropped as they come, and a gap (a reconnect, a
// dropped frame, the topic joining a live subscription) after which it reads what changed since its last seq
export type SpanSignal
  = | { kind: 'event', event: LiveEvent }
    | { kind: 'gap', cause: ResyncCause }

export interface SpanChannel {
  emit: (signal: SpanSignal) => void
  subscribe: (listener: (signal: SpanSignal) => void) => () => void
}

// span events are not kept: a page that listens folds them into its own model, and none is held for a page to come
export function createSpanChannel(): SpanChannel {
  const listeners = new Set<(signal: SpanSignal) => void>()
  return {
    emit(signal) {
      for (const listener of listeners)
        listener(signal)
    },
    subscribe(listener) {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
  }
}
