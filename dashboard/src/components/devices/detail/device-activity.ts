import type { TimelineEntry } from '@/components/data/timeline'
import type { StreamEvent } from '@/lib/live/store'
import { activityEntry } from '@/components/activity/activity-entry'

// one device's stops, newest first, without its name before each (the page is the device's)
export function deviceActivity(events: readonly StreamEvent[], deviceId: string): TimelineEntry[] {
  return events.flatMap((event) => {
    const entry = event.deviceId === deviceId && activityEntry(event)
    return entry
      ? [entry]
      : []
  })
}
