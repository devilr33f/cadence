import { ActivityIcon, UnplugIcon } from 'lucide-react'
import { useMemo } from 'react'
import { EmptyState, LoadingLines } from '@/components/data/states'
import { Timeline } from '@/components/data/timeline'
import { TIMELINE_LIMIT } from '@/lib/live/store'
import { useLiveStatus, useLiveTimeline } from '@/lib/live/use-live'
import { useTimeZone } from '@/lib/time-zone'
import { deviceActivity } from './device-activity'

// this device's turns, the assistant's steps and its media as they happen; the page's topics bring them
export function DeviceTimeline({ deviceId, deviceName }: { deviceId: string, deviceName: string }) {
  const events = useLiveTimeline()
  const status = useLiveStatus()
  const timeZone = useTimeZone()
  const entries = useMemo(() => deviceActivity(events, deviceId), [events, deviceId])

  return (
    <div className="rounded-lg border bg-card">
      {entries.length > 0 && (
        <div className="px-4 pt-4">
          <Timeline label={`${deviceName} activity`} entries={entries} live={status === 'live'} timeZone={timeZone} />
        </div>
      )}
      {entries.length === 0 && status === 'connecting' && <LoadingLines label="Connecting to the live feed" className="p-4" />}
      {entries.length === 0 && status === 'live' && (
        <EmptyState
          icon={ActivityIcon}
          title="Nothing has happened yet"
          description={`This speaker's turns, the assistant's steps and its media show here as they happen, among the tab's last ${TIMELINE_LIMIT} events.`}
        />
      )}
      {entries.length === 0 && status !== 'live' && status !== 'connecting' && (
        <EmptyState icon={UnplugIcon} title="The live feed is offline" description="Activity shows here once it reconnects." />
      )}
    </div>
  )
}
