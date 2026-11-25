// a device the feed says was deleted: a page that shows it takes the deletion over, since it must stop reading the
// device before the device's queries are dropped (a query a page still observes would be fetched again, into a 404)
export interface DeviceDeletions {
  watch: (deviceId: string, onDeleted: () => void) => () => void
  // whether a page took it
  take: (deviceId: string) => boolean
}

export function createDeviceDeletions(): DeviceDeletions {
  const watchers = new Map<string, Set<() => void>>()
  return {
    watch(deviceId, onDeleted) {
      // its own entry, so two pages that pass one function each release only their own
      const entry = () => onDeleted()
      const entries = watchers.get(deviceId) ?? new Set()
      entries.add(entry)
      watchers.set(deviceId, entries)
      return () => {
        entries.delete(entry)
        if (entries.size === 0 && watchers.get(deviceId) === entries)
          watchers.delete(deviceId)
      }
    },
    take(deviceId) {
      const entries = watchers.get(deviceId)
      if (!entries)
        return false
      for (const entry of [...entries])
        entry()
      return true
    },
  }
}
