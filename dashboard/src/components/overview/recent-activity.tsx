import { ActivityIcon, UnplugIcon } from 'lucide-react'
import { useMemo } from 'react'
import { activityEntry } from '@/components/activity/activity-entry'
import { EmptyState, LoadingLines } from '@/components/data/states'
import { Timeline } from '@/components/data/timeline'
import { useDeviceNames } from '@/hooks/use-device-names'
import { TIMELINE_LIMIT } from '@/lib/live/store'
import { useLiveStatus, useLiveTimeline } from '@/lib/live/use-live'
import { useTimeZone } from '@/lib/time-zone'

function Waiting({ live }: { live: boolean }) {
  return live
    ? (
        <EmptyState
          icon={ActivityIcon}
          title="Nothing has happened yet"
          description={`Turns, the assistant's steps and tasks show here as they happen; this page keeps the last ${TIMELINE_LIMIT} while it is open.`}
        />
      )
    : (
        <EmptyState icon={UnplugIcon} title="The live feed is offline" description="Activity shows here once it reconnects." />
      )
}

export function RecentActivity() {
  const events = useLiveTimeline()
  const status = useLiveStatus()
  const timeZone = useTimeZone()
  const names = useDeviceNames()
  // media sessions are a device page's (it follows the media topic); here they would come and go with its visits
  const entries = useMemo(() => events.flatMap((event) => {
    const entry = event.type !== 'media_state' && activityEntry(event, names)
    return entry
      ? [entry]
      : []
  }), [events, names])

  return (
    <section aria-labelledby="activity-title">
      <h2 id="activity-title" className="mb-3 text-lg font-semibold">Recent activity</h2>
      <div className="rounded-lg border bg-card">
        {entries.length > 0 && (
          <div className="px-4 pt-4">
            <Timeline label="Recent activity" entries={entries} live={status === 'live'} timeZone={timeZone} />
          </div>
        )}
        {entries.length === 0 && status === 'connecting' && <LoadingLines label="Connecting to the live feed" className="p-4" />}
        {entries.length === 0 && status !== 'connecting' && <Waiting live={status === 'live'} />}
      </div>
    </section>
  )
}
