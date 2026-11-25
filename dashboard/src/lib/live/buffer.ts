export interface BoundedBuffer<T> {
  push: (item: T) => void
  // replaces the newest item same matches in place, else pushes
  upsert: (item: T, same: (old: T) => boolean) => void
  // newest first; the same array until the next push, as useSyncExternalStore needs
  snapshot: () => readonly T[]
  subscribe: (listener: () => void) => () => void
}

// a stream's memory: at most capacity items, the oldest dropped first
export function createBoundedBuffer<T>(capacity: number): BoundedBuffer<T> {
  const items: T[] = []
  const listeners = new Set<() => void>()
  let snapshot: readonly T[] | undefined = []

  return {
    push(item) {
      items.push(item)
      if (items.length > capacity)
        items.splice(0, items.length - capacity)
      snapshot = undefined
      for (const listener of listeners)
        listener()
    },
    upsert(item, same) {
      const at = items.findLastIndex(same)
      if (at < 0) {
        this.push(item)
        return
      }
      items[at] = item
      snapshot = undefined
      for (const listener of listeners)
        listener()
    },
    snapshot() {
      snapshot ??= items.toReversed()
      return snapshot
    },
    subscribe(listener) {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
  }
}
